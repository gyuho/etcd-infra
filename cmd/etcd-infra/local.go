package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	etcdclient "git.tbd/etcd-infra/internal/etcd/client"
	"git.tbd/etcd-infra/pkg/providers/compute"
	localprovider "git.tbd/etcd-infra/pkg/providers/local"
)

const (
	containerRuntimeEnv          = "ETCD_INFRA_CONTAINER_RUNTIME"
	containerRuntimeProbeTimeout = 5 * time.Second
	defaultClusterName           = "etcd-infra"
	etcdImage                    = "gcr.io/etcd-development/etcd"
)

var clusterNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func runLocal(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: etcd-infra local <up|replace|down|status>")
	}

	switch args[0] {
	case "up":
		return runLocalUp(ctx, args[1:])
	case "replace":
		return runLocalReplace(ctx, args[1:])
	case "down":
		return runLocalDown(ctx, args[1:])
	case "status":
		return runLocalStatus(ctx, args[1:])
	default:
		return fmt.Errorf("unknown local command %q", args[0])
	}
}

func runLocalUp(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("local up", flag.ContinueOnError)
	version := flags.String("version", "latest", "etcd release version")
	name := flags.String("name", defaultClusterName, "cluster name")
	memberCount := flags.Int("members", 1, "cluster member count (1 or 3)")
	port := flags.Int("port", 2379, "first host client port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateLocalOptions(*name, *memberCount, *port); err != nil {
		return err
	}
	runtime, err := localContainerRuntime(ctx)
	if err != nil {
		return err
	}

	resolvedVersion, err := resolveVersion(ctx, *version)
	if err != nil {
		return err
	}
	members := localMembers(*name, *memberCount, *port)
	network := localprovider.NetworkName(*name)
	if output, err := exec.CommandContext(ctx, runtime, "network", "create", network).CombinedOutput(); err != nil {
		return fmt.Errorf("create %s network %s: %w: %s", runtime, network, err, strings.TrimSpace(string(output)))
	}

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = removeLocalCluster(cleanupCtx, runtime, *name)
	}
	manager := localprovider.New(runtime, *name, 0)
	for i, member := range members {
		command := append([]string{"/usr/local/bin/etcd"}, etcdServerArgs(member, members, *name, localprovider.DataDir)...)
		_, createErr := manager.Create(ctx, compute.NewCreateRequest(
			compute.WithName(member.Name),
			compute.WithImage(etcdImage+":"+releaseTag(resolvedVersion)),
			compute.WithPortMappings([]compute.PortMapping{{ContainerPort: 2379, HostPort: *port + i, HostIP: "127.0.0.1"}}),
			compute.WithProviderConfig(localprovider.CreateConfig{Command: command}),
		))
		if createErr != nil {
			cleanup()
			return fmt.Errorf("start local etcd member %s: %w", member.Name, createErr)
		}
	}

	endpoints := memberClientURLs(members)
	cli, err := etcdclient.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		cleanup()
		return fmt.Errorf("create etcd client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	if err := etcdclient.WaitForClusterHealthy(ctx, cli, endpoints, time.Minute); err != nil {
		logs := localClusterLogs(runtime, *name)
		cleanup()
		return fmt.Errorf("wait for local etcd: %w\n%s", err, logs)
	}

	fmt.Printf("local etcd %s cluster is healthy at %s\n", releaseTag(resolvedVersion), strings.Join(endpoints, ","))
	return nil
}

func runLocalDown(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("local down", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	runtimes, err := localContainerRuntimes(ctx)
	if err != nil {
		return err
	}
	var removeErr error
	for _, runtime := range runtimes {
		// A prior up may have selected a different runtime, so clean up each one.
		if err := removeLocalCluster(ctx, runtime, *name); err != nil {
			removeErr = errors.Join(removeErr, fmt.Errorf("%s: %w", runtime, err))
		}
	}
	if removeErr != nil {
		return removeErr
	}
	fmt.Printf("removed local etcd cluster %s\n", *name)
	return nil
}

func runLocalStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("local status", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	runtimes, err := localContainerRuntimes(ctx)
	if err != nil {
		return err
	}
	var outputs []string
	var inspectErr error
	for _, runtime := range runtimes {
		output, err := exec.CommandContext(
			ctx,
			runtime, "ps", "--all",
			"--filter", localprovider.ClusterFilter(*name),
			"--format", "{{.Names}}\t{{.Status}}\t{{.Ports}}",
		).CombinedOutput()
		if err != nil {
			inspectErr = errors.Join(inspectErr, fmt.Errorf("inspect local etcd with %s: %w: %s", runtime, err, strings.TrimSpace(string(output))))
			continue
		}
		if output := strings.TrimSpace(string(output)); output != "" {
			outputs = append(outputs, runtime+":\n"+output)
		}
	}
	if len(outputs) > 0 {
		fmt.Println(strings.Join(outputs, "\n"))
	}
	if inspectErr != nil {
		return inspectErr
	}
	if len(outputs) == 0 {
		return fmt.Errorf("local etcd cluster %s not found", *name)
	}
	return nil
}

func validateLocalOptions(name string, members, port int) error {
	if err := validateClusterName(name); err != nil {
		return err
	}
	if err := validateMemberCount(members); err != nil {
		return err
	}
	if port < 1 || port+members-1 > 65535 {
		return fmt.Errorf("invalid client port range %d-%d", port, port+members-1)
	}
	return nil
}

func validateClusterName(name string) error {
	if name != strings.TrimSpace(name) || !clusterNamePattern.MatchString(name) {
		return fmt.Errorf("invalid cluster name %q", name)
	}
	return nil
}

func localMembers(cluster string, count, firstPort int) []clusterMember {
	members := make([]clusterMember, 0, count)
	for i := range count {
		name := fmt.Sprintf("%s-%d", cluster, i+1)
		members = append(members, clusterMember{
			Name:      name,
			ClientURL: "http://127.0.0.1:" + strconv.Itoa(firstPort+i),
			PeerURL:   "http://" + name + ":2380",
		})
	}
	return members
}

func memberClientURLs(members []clusterMember) []string {
	endpoints := make([]string, 0, len(members))
	for _, member := range members {
		endpoints = append(endpoints, member.ClientURL)
	}
	return endpoints
}

func removeLocalCluster(ctx context.Context, runtime, cluster string) error {
	var cleanupErr error
	output, err := exec.CommandContext(
		ctx,
		runtime, "ps", "--all", "--quiet", "--filter", localprovider.ClusterFilter(cluster),
	).CombinedOutput()
	if err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list local etcd containers: %w: %s", err, strings.TrimSpace(string(output))))
	} else if ids := strings.Fields(string(output)); len(ids) > 0 {
		rmArgs := append([]string{"rm", "--force"}, ids...)
		if rmOutput, rmErr := exec.CommandContext(ctx, runtime, rmArgs...).CombinedOutput(); rmErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove local etcd containers: %w: %s", rmErr, strings.TrimSpace(string(rmOutput))))
		}
	}

	volumeOutput, volumeErr := exec.CommandContext(
		ctx,
		runtime, "volume", "ls", "--quiet", "--filter", localprovider.ClusterFilter(cluster),
	).CombinedOutput()
	if volumeErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list local etcd volumes: %w: %s", volumeErr, strings.TrimSpace(string(volumeOutput))))
	} else if volumes := strings.Fields(string(volumeOutput)); len(volumes) > 0 {
		rmArgs := append([]string{"volume", "rm", "--force"}, volumes...)
		if rmOutput, rmErr := exec.CommandContext(ctx, runtime, rmArgs...).CombinedOutput(); rmErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove local etcd volumes: %w: %s", rmErr, strings.TrimSpace(string(rmOutput))))
		}
	}

	network := localprovider.NetworkName(cluster)
	networkOutput, networkErr := exec.CommandContext(ctx, runtime, "network", "rm", network).CombinedOutput()
	if networkErr != nil && !strings.Contains(strings.ToLower(string(networkOutput)), "not found") {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s network %s: %w: %s", runtime, network, networkErr, strings.TrimSpace(string(networkOutput))))
	}
	return cleanupErr
}

func localClusterLogs(runtime, cluster string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, err := exec.CommandContext(
		ctx,
		runtime, "ps", "--all", "--quiet", "--filter", localprovider.ClusterFilter(cluster),
	).CombinedOutput()
	if err != nil {
		return "unable to list container logs: " + err.Error()
	}
	var logs strings.Builder
	for _, id := range strings.Fields(string(output)) {
		memberLogs, _ := exec.CommandContext(ctx, runtime, "logs", id).CombinedOutput()
		logs.Write(memberLogs)
	}
	return strings.TrimSpace(logs.String())
}

func localContainerRuntime(ctx context.Context) (string, error) {
	runtimes, err := localContainerRuntimes(ctx)
	if err != nil {
		return "", err
	}
	return runtimes[0], nil
}

func localContainerRuntimes(ctx context.Context) ([]string, error) {
	configured := os.Getenv(containerRuntimeEnv)
	if configured != "" && configured != "docker" && configured != "podman" {
		return nil, fmt.Errorf("%s must be docker or podman", containerRuntimeEnv)
	}

	candidates := []string{"docker", "podman"}
	if configured != "" {
		candidates = []string{configured}
	}
	var runtimes []string
	for _, runtime := range candidates {
		if _, err := exec.LookPath(runtime); err != nil {
			continue
		}
		// Bound each probe so an unresponsive runtime does not block fallback.
		probeCtx, cancel := context.WithTimeout(ctx, containerRuntimeProbeTimeout)
		err := exec.CommandContext(probeCtx, runtime, "info").Run()
		cancel()
		if err == nil {
			runtimes = append(runtimes, runtime)
		}
	}
	if len(runtimes) > 0 {
		return runtimes, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if configured != "" {
		return nil, fmt.Errorf("%s=%s is not installed or not running", containerRuntimeEnv, configured)
	}
	return nil, errors.New("Docker or Podman must be installed and running")
}
