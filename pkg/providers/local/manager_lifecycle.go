package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

// CreateConfig contains the container-specific fields for Create.
type CreateConfig struct {
	Command []string
	// Env carries KEY=VALUE environment variables into the container.
	Env []string
	// AuxPortMapping optionally publishes one extra container port (for
	// example a gofail HTTP endpoint) alongside the etcd client port.
	AuxPortMapping *compute.PortMapping
}

// Create starts a container on the cluster network with a persistent data volume.
func (m *Manager) Create(ctx context.Context, req compute.CreateRequest) (compute.Instance, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Op.Name)
	if name == "" {
		return nil, errors.New("local: container name is required")
	}
	if strings.TrimSpace(req.Op.Image) == "" {
		return nil, errors.New("local: container image is required")
	}
	if exec.CommandContext(ctx, m.runtime, "inspect", name).Run() == nil {
		return nil, fmt.Errorf("local: container %s already exists", name)
	}
	cfg, ok := req.Op.ProviderConfig.(CreateConfig)
	if !ok || len(cfg.Command) == 0 {
		return nil, errors.New("local: valid CreateConfig is required")
	}
	port, err := clientPortMapping(req.Op.PortMappings)
	if err != nil {
		return nil, err
	}
	if err := validateCreateConfig(cfg); err != nil {
		return nil, err
	}

	volume := dataVolumeName(name)
	if exec.CommandContext(ctx, m.runtime, "volume", "inspect", volume).Run() == nil {
		return nil, fmt.Errorf("local: data volume %s already exists", volume)
	}
	output, err := exec.CommandContext(ctx, m.runtime, "volume", "create", "--label", clusterLabel(m.cluster), volume).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("local: create data volume for %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	created := false
	defer func() {
		if created {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		// The existence checks above race with external actors, so the failure
		// may be a name conflict with resources this manager never created.
		// Remove only resources carrying this cluster's label. The volume check
		// matters because Docker's volume create is idempotent and can adopt a
		// pre-existing volume, which would not carry the label.
		if _, inspectErr := m.ownedContainer(cleanupCtx, name); inspectErr == nil {
			_, _ = exec.CommandContext(cleanupCtx, m.runtime, "rm", "--force", name).CombinedOutput()
		}
		if m.ownedVolume(cleanupCtx, volume) {
			_, _ = exec.CommandContext(cleanupCtx, m.runtime, "volume", "rm", "--force", volume).CombinedOutput()
		}
	}()
	output, err = exec.CommandContext(ctx, m.runtime, createRunArgs(m.cluster, name, req.Op.Image, volume, port, cfg)...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("local: start container %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	instance, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	created = true
	return instance, nil
}

// ownedVolume reports whether a volume exists and carries this cluster's
// ownership label.
func (m *Manager) ownedVolume(ctx context.Context, name string) bool {
	output, err := exec.CommandContext(ctx, m.runtime, "volume", "inspect", name).CombinedOutput()
	if err != nil {
		return false
	}
	var inspected []struct {
		Labels map[string]string
	}
	if err := json.Unmarshal(output, &inspected); err != nil || len(inspected) != 1 {
		return false
	}
	return inspected[0].Labels[clusterLabelKey] == m.cluster
}

// Delete removes a container but leaves its named data volume to cluster cleanup.
func (m *Manager) Delete(ctx context.Context, req compute.DeleteRequest) (compute.DeleteResult, error) {
	if err := m.validate(); err != nil {
		return compute.DeleteResult{}, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return compute.DeleteResult{}, errors.New("local: container name is required")
	}
	if _, err := m.ownedContainer(ctx, id); err != nil {
		return compute.DeleteResult{}, err
	}
	output, err := exec.CommandContext(ctx, m.runtime, "rm", "--force", id).CombinedOutput()
	if err != nil {
		return compute.DeleteResult{}, fmt.Errorf("local: remove container %s: %w: %s", id, err, strings.TrimSpace(string(output)))
	}
	return compute.DeleteResult{ID: id, Deleted: true}, nil
}

// ReplaceMachine recreates a container with its existing IP, data volume,
// image, command, host port, and cluster ownership label. Termination and
// recovery run to completion even if the caller's context is canceled
// mid-operation, so a nil error always means the member was replaced; the
// caller observes its own cancellation separately.
func (m *Manager) ReplaceMachine(ctx context.Context, req compute.ReplaceRequest) (compute.ReplaceResult, error) {
	if err := m.validate(); err != nil {
		return compute.ReplaceResult{}, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return compute.ReplaceResult{}, errors.New("local: container name is required")
	}
	if err := ctx.Err(); err != nil {
		return compute.ReplaceResult{}, err
	}

	inspected, err := m.ownedContainer(ctx, id)
	if err != nil {
		return compute.ReplaceResult{}, err
	}
	before, err := replacementSpecFromInspect(inspected, m.cluster)
	if err != nil {
		return compute.ReplaceResult{}, err
	}
	terminationCtx, cancelTermination := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	output, err := exec.CommandContext(terminationCtx, m.runtime, "rm", "--force", before.name).CombinedOutput()
	cancelTermination()
	if err != nil {
		return compute.ReplaceResult{}, fmt.Errorf("local: terminate container %s: %w: %s", before.name, err, strings.TrimSpace(string(output)))
	}
	var callerErr error
	if m.downtime > 0 {
		timer := time.NewTimer(m.downtime)
		select {
		case <-timer.C:
		case <-ctx.Done():
			callerErr = ctx.Err()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	// The recovery budget tolerates container runtimes that stall `run` under
	// sustained load (observed: Podman VMs on macOS intermittently exceeding
	// 30s during long E2E runs); a slow relaunch is infrastructure noise, not
	// a replacement failure.
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), 120*time.Second)
	defer cancelRecovery()
	recoveryErr := m.restoreContainer(recoveryCtx, before)
	if recoveryErr != nil {
		// The member may be down; surface a caller cancellation alongside the
		// recovery failure instead of hiding either.
		if callerErr == nil {
			callerErr = ctx.Err()
		}
		return compute.ReplaceResult{}, errors.Join(callerErr, recoveryErr)
	}
	return compute.ReplaceResult{PreviousID: id}, nil
}

func (m *Manager) restoreContainer(ctx context.Context, before replacementSpec) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		inspected, inspectErr := m.ownedContainer(ctx, before.name)
		switch {
		case inspectErr == nil && !inspected.State.Running:
			switch strings.ToLower(inspected.State.Status) {
			case "created", "configured", "restarting":
				// The runtime can report success before the container flips to
				// running (Podman calls the pre-start state "configured");
				// recheck on the next tick.
				lastErr = fmt.Errorf("local: container %s is %s", before.name, inspected.State.Status)
			default:
				// A relaunched etcd that already exited, or a partial creation
				// the runtime left behind. Remove it so the relaunch can reuse
				// the name.
				output, err := runWithAttemptTimeout(ctx, m.runtime, "rm", "--force", before.name)
				if err != nil {
					lastErr = fmt.Errorf("local: remove stopped container %s: %w: %s", before.name, err, output)
				} else {
					lastErr = fmt.Errorf("local: container %s stopped (%s); relaunching", before.name, inspected.State.Status)
				}
			}
		case inspectErr == nil:
			after, err := replacementSpecFromInspect(inspected, m.cluster)
			if err != nil {
				// Network settings can lag container start on some runtimes, so
				// an inspect anomaly is retried within the recovery budget
				// rather than aborting the replacement.
				lastErr = err
				break
			}
			if after.containerID == before.containerID {
				return fmt.Errorf("local: container %s was not replaced", before.name)
			}
			if after.privateIP != before.privateIP || after.volume != before.volume ||
				after.auxPublish != before.auxPublish || !slices.Equal(after.env, before.env) {
				// The running container violates the replacement identity and
				// must not serve the member. It carries this cluster's label
				// (ownedContainer passed), so remove it and let the next tick
				// relaunch with the pinned identity inside the budget.
				mismatchErr := fmt.Errorf(
					"local: container %s replacement identity changed: ip %s -> %s, volume %s -> %s, aux port %q -> %q, env %v -> %v",
					before.name, before.privateIP, after.privateIP, before.volume, after.volume,
					before.auxPublish, after.auxPublish, before.env, after.env,
				)
				output, err := runWithAttemptTimeout(ctx, m.runtime, "rm", "--force", before.name)
				if err != nil {
					return fmt.Errorf("%w; remove mismatched container: %v: %s", mismatchErr, err, output)
				}
				lastErr = mismatchErr
				break
			}
			return nil
		default:
			lastErr = inspectErr
			output, runErr := runWithAttemptTimeout(ctx, m.runtime, before.runArgs()...)
			if runErr != nil {
				lastErr = fmt.Errorf("local: relaunch container %s with preserved volume %s: %w: %s", before.name, before.volume, runErr, output)
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("local: recover container %s: %w: %v", before.name, ctx.Err(), lastErr)
		}
	}
}

// runWithAttemptTimeout runs one container-runtime command with its own
// attempt budget. A container runtime can occasionally wedge a single command
// (observed: Podman on macOS hanging `run` for minutes while the rest of the
// VM stays healthy); without a per-attempt budget one wedged command consumes
// the whole recovery budget and fails a replacement that an immediate retry
// would complete.
func runWithAttemptTimeout(ctx context.Context, runtime string, args ...string) (string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(attemptCtx, runtime, args...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

type replacementSpec struct {
	containerID string
	name        string
	cluster     string
	image       string
	command     []string
	env         []string
	auxPublish  string
	network     string
	privateIP   string
	hostIP      string
	hostPort    string
	volume      string
}

func replacementSpecFromInspect(inspected containerInspect, cluster string) (replacementSpec, error) {
	name := strings.TrimPrefix(inspected.Name, "/")
	networkName := NetworkName(cluster)
	network, ok := inspected.NetworkSettings.Networks[networkName]
	if !ok || network.IPAddress == "" {
		return replacementSpec{}, fmt.Errorf("local: container %s has no IP on network %s", name, networkName)
	}
	bindings := inspected.HostConfig.PortBindings["2379/tcp"]
	if len(bindings) != 1 || bindings[0].HostPort == "" {
		return replacementSpec{}, fmt.Errorf("local: container %s has no single client port binding", name)
	}
	auxPublish := ""
	for portKey, portBindings := range inspected.HostConfig.PortBindings {
		if portKey == "2379/tcp" {
			continue
		}
		containerPort, found := strings.CutSuffix(portKey, "/tcp")
		if !found || len(portBindings) != 1 || portBindings[0].HostPort == "" {
			return replacementSpec{}, fmt.Errorf("local: container %s has invalid auxiliary port binding %s", name, portKey)
		}
		if auxPublish != "" {
			return replacementSpec{}, fmt.Errorf("local: container %s has more than one auxiliary port binding", name)
		}
		hostIP := portBindings[0].HostIP
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		auxPublish = hostIP + ":" + portBindings[0].HostPort + ":" + containerPort
	}
	volume := ""
	for _, mount := range inspected.Mounts {
		if mount.Type == "volume" && mount.Destination == DataDir {
			volume = mount.Name
			break
		}
	}
	if volume == "" {
		return replacementSpec{}, fmt.Errorf("local: container %s has no named data volume at %s", name, DataDir)
	}
	if inspected.ID == "" || name == "" || inspected.Config.Image == "" || len(inspected.Config.Cmd) == 0 {
		return replacementSpec{}, fmt.Errorf("local: container %s inspection is incomplete", name)
	}
	return replacementSpec{
		containerID: inspected.ID,
		name:        name,
		cluster:     cluster,
		image:       inspected.Config.Image,
		command:     inspected.Config.Cmd,
		env:         inspected.Config.Env,
		auxPublish:  auxPublish,
		network:     networkName,
		privateIP:   network.IPAddress,
		hostIP:      bindings[0].HostIP,
		hostPort:    bindings[0].HostPort,
		volume:      volume,
	}, nil
}

func clientPortMapping(mappings []compute.PortMapping) (compute.PortMapping, error) {
	if len(mappings) != 1 {
		return compute.PortMapping{}, errors.New("local: exactly one client port mapping is required")
	}
	mapping := mappings[0]
	if mapping.ContainerPort != 2379 || mapping.HostPort < 1 || mapping.HostPort > 65535 {
		return compute.PortMapping{}, errors.New("local: a valid 2379/tcp port mapping is required")
	}
	if mapping.Protocol != "" && mapping.Protocol != "tcp" {
		return compute.PortMapping{}, errors.New("local: client port mapping must use tcp")
	}
	return mapping, nil
}

func validateCreateConfig(cfg CreateConfig) error {
	for _, env := range cfg.Env {
		key, _, found := strings.Cut(env, "=")
		if !found || key == "" {
			return fmt.Errorf("local: invalid environment entry %q", env)
		}
	}
	if cfg.AuxPortMapping == nil {
		return nil
	}
	aux := *cfg.AuxPortMapping
	if aux.ContainerPort < 1 || aux.ContainerPort > 65535 || aux.ContainerPort == 2379 {
		return fmt.Errorf("local: invalid auxiliary container port %d", aux.ContainerPort)
	}
	if aux.HostPort < 1 || aux.HostPort > 65535 {
		return fmt.Errorf("local: invalid auxiliary host port %d", aux.HostPort)
	}
	if aux.Protocol != "" && aux.Protocol != "tcp" {
		return errors.New("local: auxiliary port mapping must use tcp")
	}
	return nil
}

func publishArg(mapping compute.PortMapping, hostPort int) string {
	hostIP := mapping.HostIP
	if hostIP == "" {
		hostIP = "127.0.0.1"
	}
	return hostIP + ":" + strconv.Itoa(hostPort) + ":" + strconv.Itoa(mapping.ContainerPort)
}

func createRunArgs(cluster, name, image, volume string, port compute.PortMapping, cfg CreateConfig) []string {
	// No --rm: Stop/Start are part of the provider contract, and an
	// auto-removing container would make Stop delete the machine. Removal is
	// always explicit through Delete or cluster cleanup.
	args := []string{
		"run", "--detach",
		"--name", name,
		"--label", clusterLabel(cluster),
		"--network", NetworkName(cluster),
		"--publish", publishArg(port, port.HostPort),
	}
	if cfg.AuxPortMapping != nil {
		args = append(args, "--publish", publishArg(*cfg.AuxPortMapping, cfg.AuxPortMapping.HostPort))
	}
	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	args = append(args,
		"--volume", volume+":"+DataDir,
		image,
	)
	return append(args, cfg.Command...)
}

func (spec replacementSpec) runArgs() []string {
	publish := spec.hostPort + ":2379"
	if spec.hostIP != "" {
		publish = spec.hostIP + ":" + publish
	}
	args := []string{
		"run", "--detach",
		"--name", spec.name,
		"--label", clusterLabel(spec.cluster),
		"--network", spec.network,
		"--ip", spec.privateIP,
		"--publish", publish,
	}
	if spec.auxPublish != "" {
		args = append(args, "--publish", spec.auxPublish)
	}
	for _, env := range spec.env {
		args = append(args, "--env", env)
	}
	args = append(args,
		"--volume", spec.volume+":"+DataDir,
		spec.image,
	)
	return append(args, spec.command...)
}
