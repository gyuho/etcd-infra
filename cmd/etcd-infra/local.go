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
		return errors.New("usage: etcd-infra local <up|down|status>")
	}

	switch args[0] {
	case "up":
		return runLocalUp(ctx, args[1:])
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
	network := localNetworkName(*name)
	if output, err := exec.CommandContext(ctx, runtime, "network", "create", network).CombinedOutput(); err != nil {
		return fmt.Errorf("create %s network %s: %w: %s", runtime, network, err, strings.TrimSpace(string(output)))
	}

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = removeLocalCluster(cleanupCtx, runtime, *name)
	}
	for i, member := range members {
		output, runErr := exec.CommandContext(
			ctx,
			runtime,
			containerRunArgs(member, members, *name, *port+i, resolvedVersion)...,
		).CombinedOutput()
		if runErr != nil {
			cleanup()
			return fmt.Errorf("start local etcd member %s: %w: %s", member.Name, runErr, strings.TrimSpace(string(output)))
		}
	}

	endpoints := memberClientURLs(members)
	cli, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
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
			"--filter", localClusterFilter(*name),
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

func containerRunArgs(member clusterMember, members []clusterMember, cluster string, port int, version string) []string {
	args := []string{
		"run", "--detach", "--rm",
		"--name", member.Name,
		"--label", localClusterLabel(cluster),
		"--network", localNetworkName(cluster),
		"--publish", "127.0.0.1:" + strconv.Itoa(port) + ":2379",
		etcdImage + ":" + releaseTag(version),
		"/usr/local/bin/etcd",
	}
	return append(args, etcdServerArgs(member, members, cluster, "/etcd-data")...)
}

func localNetworkName(cluster string) string {
	return cluster + "-net"
}

func localClusterLabel(cluster string) string {
	return "etcd-infra.cluster=" + cluster
}

func localClusterFilter(cluster string) string {
	return "label=" + localClusterLabel(cluster)
}

func removeLocalCluster(ctx context.Context, runtime, cluster string) error {
	output, err := exec.CommandContext(
		ctx,
		runtime, "ps", "--all", "--quiet", "--filter", localClusterFilter(cluster),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("list local etcd containers: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if ids := strings.Fields(string(output)); len(ids) > 0 {
		rmArgs := append([]string{"rm", "--force"}, ids...)
		if rmOutput, rmErr := exec.CommandContext(ctx, runtime, rmArgs...).CombinedOutput(); rmErr != nil {
			return fmt.Errorf("remove local etcd containers: %w: %s", rmErr, strings.TrimSpace(string(rmOutput)))
		}
	}

	network := localNetworkName(cluster)
	networkOutput, networkErr := exec.CommandContext(ctx, runtime, "network", "rm", network).CombinedOutput()
	if networkErr != nil && !strings.Contains(strings.ToLower(string(networkOutput)), "not found") {
		return fmt.Errorf("remove %s network %s: %w: %s", runtime, network, networkErr, strings.TrimSpace(string(networkOutput)))
	}
	return nil
}

func localClusterLogs(runtime, cluster string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, err := exec.CommandContext(
		ctx,
		runtime, "ps", "--all", "--quiet", "--filter", localClusterFilter(cluster),
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
