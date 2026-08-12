package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	etcdclient "git.tbd/etcd-infra/internal/etcd/client"
	"git.tbd/etcd-infra/pkg/providers/compute"
	localprovider "git.tbd/etcd-infra/pkg/providers/local"
)

const (
	localReplacementDowntime = 3 * time.Second
	localReplacementTimeout  = time.Minute
	leaderChangeTimeout      = 10 * time.Second
)

// waitForLeaderChange polls until the cluster has a leader other than the
// replaced member or maxWait elapses, and returns the last observed leader.
func waitForLeaderChange(ctx context.Context, cli *clientv3.Client, members []clusterMember, replaced string, maxWait time.Duration) (string, error) {
	deadline := time.Now().Add(maxWait)
	var lastLeader string
	var lastErr error
	for {
		leader, err := localLeaderName(ctx, cli, members)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			lastLeader = leader
			if leader != replaced {
				return leader, nil
			}
		}
		if time.Now().After(deadline) {
			if lastLeader == "" {
				return "", lastErr
			}
			return lastLeader, nil
		}
		select {
		case <-time.After(etcdclient.DefaultClusterHealthPollInterval):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func runLocalReplace(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("local replace", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	memberName := flags.String("member", "leader", "member name, or leader")
	memberCount := flags.Int("members", 1, "cluster member count (1 or 3)")
	port := flags.Int("port", 2379, "first host client port")
	downtime := flags.Duration("downtime", localReplacementDowntime, "minimum time before recreating the container")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateLocalOptions(*name, *memberCount, *port); err != nil {
		return err
	}
	if *downtime < 0 {
		return errors.New("downtime must be non-negative")
	}

	members := localMembers(*name, *memberCount, *port)
	endpoints := memberClientURLs(members)
	cli, err := etcdclient.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("create etcd client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	if err := waitForLocalEndpointsHealthy(ctx, cli, endpoints, localReplacementTimeout); err != nil {
		return err
	}

	target := strings.TrimSpace(*memberName)
	replacingLeader := target == "leader"
	if replacingLeader {
		target, err = localLeaderName(ctx, cli, members)
		if err != nil {
			return err
		}
	} else if !hasLocalMember(members, target) {
		return fmt.Errorf("member %q is not part of cluster %s", target, *name)
	}

	runtime, err := localRuntimeForContainer(ctx, target)
	if err != nil {
		return err
	}
	if _, err := localprovider.New(runtime, *name, *downtime).ReplaceMachine(ctx, compute.NewReplaceRequest(target)); err != nil {
		return err
	}
	if err := waitForLocalEndpointsHealthy(ctx, cli, endpoints, localReplacementTimeout); err != nil {
		return fmt.Errorf("wait for replaced member %s: %w", target, err)
	}

	if replacingLeader && len(members) > 1 {
		newLeader, err := waitForLeaderChange(ctx, cli, members, target, leaderChangeTimeout)
		if err != nil {
			return err
		}
		if newLeader == target {
			// Raft may legally re-elect the replaced member after it catches up,
			// so an unchanged leader is a warning, not a replacement failure.
			fmt.Printf("replaced local etcd leader %s; leadership stayed with %s after %s; cluster healthy (client=%s)\n", target, newLeader, leaderChangeTimeout, etcdclient.Mode)
			return nil
		}
		fmt.Printf("replaced local etcd leader %s; new leader %s; cluster healthy (client=%s)\n", target, newLeader, etcdclient.Mode)
		return nil
	}

	fmt.Printf("replaced local etcd member %s; cluster healthy (client=%s)\n", target, etcdclient.Mode)
	return nil
}

func hasLocalMember(members []clusterMember, name string) bool {
	for _, member := range members {
		if member.Name == name {
			return true
		}
	}
	return false
}

func localLeaderName(ctx context.Context, cli *clientv3.Client, members []clusterMember) (string, error) {
	var lastErr error
	for _, member := range members {
		statusCtx, cancel := context.WithTimeout(ctx, etcdclient.DefaultClusterHealthStatusTimeout)
		status, err := cli.Status(statusCtx, member.ClientURL)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if status.Leader != 0 && status.Header != nil && status.Header.MemberId == status.Leader {
			return member.Name, nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("find local etcd leader: %w", lastErr)
	}
	return "", errors.New("local etcd cluster has no elected leader")
}

func waitForLocalEndpointsHealthy(ctx context.Context, cli *clientv3.Client, endpoints []string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		allHealthy := true
		for _, endpoint := range endpoints {
			statusCtx, cancel := context.WithTimeout(ctx, etcdclient.DefaultClusterHealthStatusTimeout)
			_, err := cli.Status(statusCtx, endpoint)
			cancel()
			if err != nil {
				allHealthy = false
				lastErr = err
			}
		}
		if allHealthy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local etcd endpoints did not all become healthy after %s: %w", maxWait, lastErr)
		}
		select {
		case <-time.After(etcdclient.DefaultClusterHealthPollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func localRuntimeForContainer(ctx context.Context, name string) (string, error) {
	runtimes, err := localContainerRuntimes(ctx)
	if err != nil {
		return "", err
	}
	for _, runtime := range runtimes {
		if exec.CommandContext(ctx, runtime, "inspect", name).Run() == nil {
			return runtime, nil
		}
	}
	return "", fmt.Errorf("local etcd container %s not found", name)
}
