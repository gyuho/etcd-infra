package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// runAWSReplace replaces one member's machine while preserving its identity
// (name, private IP) and data (the dedicated data volume created by
// "aws up --replaceable"). The replacement boots with the same data dir and
// rejoins as the same member.
func runAWSReplace(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws replace", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	member := flags.String("member", "leader", "member name, or \"leader\"")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	statePath, err := awsStatePath(*name)
	if err != nil {
		return err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		return err
	}
	if !state.Replaceable {
		return fmt.Errorf("cluster %s was not created with --replaceable; its members have no data volume to preserve", state.Name)
	}
	if state.BinaryURL != "" {
		return fmt.Errorf("cluster %s runs a custom binary; replacement is unsupported because the presigned URL expires", state.Name)
	}

	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	manager := awsprovider.New(cfg)

	idx, err := awsReplaceMemberIndex(ctx, state, *member)
	if err != nil {
		return err
	}
	saved := state.Instances[idx]

	volumeID, err := manager.DataVolumeID(ctx, saved.ID)
	if err != nil {
		return fmt.Errorf("check %s data volume: %w", saved.Name, err)
	}
	if volumeID == "" {
		return fmt.Errorf("%s has no data volume; recreate the cluster with --replaceable", saved.Name)
	}

	fmt.Printf("replacing %s (%s), preserving data volume %s and private IP %s\n",
		saved.Name, saved.ID, volumeID, saved.PrivateIPv4)
	if _, err := awsReplaceMember(ctx, statePath, state, idx, manager); err != nil {
		return err
	}
	fmt.Printf("replaced %s: %s -> %s; cluster is healthy\n", saved.Name, saved.ID, state.Instances[idx].ID)
	return nil
}

// awsReplaceMember replaces the machine of state.Instances[idx], re-bootstraps
// the replacement with the recorded spec, updates the state file, and returns
// the ready replacement instance. Shared by "aws replace" and the AWS
// replacement E2E tests.
func awsReplaceMember(ctx context.Context, statePath string, state awsState, idx int, manager *awsprovider.Manager) (compute.Instance, error) {
	saved := state.Instances[idx]

	result, err := manager.ReplaceMachine(ctx, compute.NewReplaceRequest(saved.ID))
	if err != nil {
		return nil, fmt.Errorf("replace %s: %w", saved.Name, err)
	}
	if result.ID == "" {
		return nil, fmt.Errorf("replace %s: the provider returned no replacement handle", saved.Name)
	}

	state.Instances[idx].ID = string(result.ID)
	if err := writeAWSState(statePath, state); err != nil {
		return nil, fmt.Errorf("save AWS cluster state: %w (replacement instance is %s)", err, result.ID)
	}

	instance, err := manager.WaitForReady(ctx, result.ID, awsReadyTimeout)
	if err != nil {
		return nil, awsSetupError(statePath, state, fmt.Errorf("wait for replacement %s: %w", result.ID, err))
	}
	state.Instances[idx].PrivateIPv4 = instance.PrivateIPv4()
	state.Instances[idx].PublicIPv4 = instance.PublicIPv4()
	if err := writeAWSState(statePath, state); err != nil {
		return nil, awsSetupError(statePath, state, fmt.Errorf("save resolved AWS cluster state: %w", err))
	}

	// Re-bootstrap with the recorded spec: the data volume remounts with the
	// existing filesystem, and etcd restarts with the member's data dir.
	members := awsMembers(state)
	arch := state.Arch
	if arch == "" {
		arch = "amd64"
	}
	bootstrap := awsBootstrapOptions{
		Version:         state.Version,
		Arch:            arch,
		ExtraArgs:       state.ExtraArgs,
		Env:             state.Env,
		DataVolumeSetup: true,
	}
	commandResult, err := instance.RunCommandWithOptions(
		ctx,
		[]string{"bash", "-ceu", awsBootstrapScript(members[idx], members, state.Name, bootstrap)},
		&compute.RunCommandOptions{Timeout: 10 * time.Minute},
	)
	if err != nil {
		return nil, awsSetupError(statePath, state, fmt.Errorf("configure replacement %s: %w", saved.Name, err))
	}
	if commandResult.ExitCode != 0 {
		return nil, awsSetupError(statePath, state,
			fmt.Errorf("configure replacement %s: exit %d: %s", saved.Name, commandResult.ExitCode, strings.TrimSpace(commandResult.Stderr)))
	}

	healthResult, err := instance.RunCommandWithOptions(
		ctx,
		[]string{"bash", "-ceu", awsHealthScript(memberClientURLs(members))},
		&compute.RunCommandOptions{Timeout: 2 * time.Minute},
	)
	if err != nil {
		return nil, awsSetupError(statePath, state, fmt.Errorf("check AWS etcd health: %w", err))
	}
	if healthResult.ExitCode != 0 {
		return nil, awsSetupError(statePath, state,
			fmt.Errorf("check AWS etcd health: exit %d: %s", healthResult.ExitCode, strings.TrimSpace(healthResult.Stderr)))
	}
	return instance, nil
}

// awsReplaceMemberIndex resolves --member to an index in state.Instances. The
// value "leader" queries the cluster over client endpoints (bastion tunnels
// when the cluster has a bastion).
func awsReplaceMemberIndex(ctx context.Context, state awsState, member string) (int, error) {
	member = strings.TrimSpace(member)
	if member != "" && member != "leader" {
		for i, instance := range state.Instances {
			if instance.Name == member {
				return i, nil
			}
		}
		return -1, fmt.Errorf("member %q not found in cluster %s", member, state.Name)
	}

	endpoints, stop, err := awsClientEndpoints(ctx, state)
	if err != nil {
		return -1, err
	}
	defer stop()

	cli, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		return -1, fmt.Errorf("create etcd client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	var leaderID uint64
	for _, endpoint := range endpoints {
		statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		status, statusErr := cli.Status(statusCtx, endpoint)
		cancel()
		if statusErr != nil {
			continue
		}
		leaderID = status.Leader
		break
	}
	if leaderID == 0 {
		return -1, errors.New("cluster has no leader to replace")
	}
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	listResp, err := cli.MemberList(listCtx)
	cancel()
	if err != nil {
		return -1, fmt.Errorf("list members: %w", err)
	}
	for _, m := range listResp.Members {
		if m.ID != leaderID {
			continue
		}
		for i, instance := range state.Instances {
			if instance.Name == m.Name {
				return i, nil
			}
		}
		return -1, fmt.Errorf("leader %q is not a member of cluster %s", m.Name, state.Name)
	}
	return -1, fmt.Errorf("leader id %d not found in membership", leaderID)
}

// awsClientEndpoints returns client endpoints for the cluster: bastion
// tunnels when the cluster has a bastion, otherwise direct member IPs
// (public when every member has one, private otherwise). The returned stop
// function releases any tunnels.
func awsClientEndpoints(ctx context.Context, state awsState) ([]string, func(), error) {
	if state.Bastion == nil {
		usePublic := true
		for _, instance := range state.Instances {
			if instance.PublicIPv4 == "" {
				usePublic = false
			}
		}
		endpoints := make([]string, 0, len(state.Instances))
		for _, instance := range state.Instances {
			ip := instance.PrivateIPv4
			if usePublic {
				ip = instance.PublicIPv4
			}
			if ip == "" {
				return nil, nil, fmt.Errorf("%s has no reachable IP", instance.Name)
			}
			endpoints = append(endpoints, "http://"+ip+":2379")
		}
		return endpoints, func() {}, nil
	}

	stops := make([]awsTunnelStop, 0, len(state.Instances))
	stopAll := func() {
		for _, stop := range stops {
			stop()
		}
	}
	endpoints := make([]string, 0, len(state.Instances))
	for _, member := range state.Instances {
		endpoint, stop, err := startAWSSSMPortForward(ctx, state.Region, state.Bastion.ID, member.PrivateIPv4, 2379)
		if err != nil {
			stopAll()
			return nil, nil, fmt.Errorf("tunnel to %s: %w", member.Name, err)
		}
		stops = append(stops, stop)
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, stopAll, nil
}
