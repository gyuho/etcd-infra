// Package local manages etcd-infra containers with Docker or Podman.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

const (
	clusterLabelKey = "etcd-infra.cluster"
	DataDir         = "/etcd-data"
)

// Manager manages one local etcd-infra cluster.
type Manager struct {
	runtime  string
	cluster  string
	downtime time.Duration
}

var _ compute.Provider = (*Manager)(nil)

// New returns a local container provider for one cluster.
func New(runtime, cluster string, downtime time.Duration) *Manager {
	return &Manager{runtime: runtime, cluster: cluster, downtime: downtime}
}

// NetworkName returns the dedicated container network for a cluster.
func NetworkName(cluster string) string { return cluster + "-net" }

// ClusterFilter returns the runtime filter for resources owned by a cluster.
func ClusterFilter(cluster string) string { return "label=" + clusterLabel(cluster) }

func dataVolumeName(member string) string { return member + "-data" }
func clusterLabel(cluster string) string  { return clusterLabelKey + "=" + cluster }

// Stop stops a container without deleting it.
func (m *Manager) Stop(ctx context.Context, req compute.PowerRequest) error {
	return m.power(ctx, "stop", req.ID)
}

// Start starts a stopped container.
func (m *Manager) Start(ctx context.Context, req compute.PowerRequest) error {
	return m.power(ctx, "start", req.ID)
}

func (m *Manager) power(ctx context.Context, operation string, id compute.InstanceHandle) error {
	if err := m.validate(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("local: container name is required")
	}
	if _, err := m.ownedContainer(ctx, id); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, m.runtime, operation, id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("local: %s container %s: %w: %s", operation, id, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Get returns one cluster container.
func (m *Manager) Get(ctx context.Context, id compute.InstanceHandle) (compute.Instance, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("local: container name is required")
	}
	inspected, err := m.ownedContainer(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.instanceFromInspect(inspected), nil
}

// List returns every container owned by the cluster.
func (m *Manager) List(ctx context.Context) ([]compute.Instance, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	output, err := exec.CommandContext(ctx, m.runtime, "ps", "--all", "--quiet", "--filter", ClusterFilter(m.cluster)).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("local: list cluster containers: %w: %s", err, strings.TrimSpace(string(output)))
	}
	ids := strings.Fields(string(output))
	instances := make([]compute.Instance, 0, len(ids))
	for _, id := range ids {
		instance, err := m.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID() < instances[j].ID() })
	return instances, nil
}

// Capabilities reports the local provider's core execution surface.
func (m *Manager) Capabilities() compute.CapabilitySet {
	return compute.NewCapabilitySet(
		compute.CapabilityLifecycleCreateDelete,
		compute.CapabilityPowerControl,
		compute.CapabilityInventoryRead,
		compute.CapabilityCommandExecution,
	)
}

func (m *Manager) validate() error {
	if m == nil || strings.TrimSpace(m.runtime) == "" {
		return errors.New("local: container runtime is required")
	}
	if strings.TrimSpace(m.cluster) == "" {
		return errors.New("local: cluster name is required")
	}
	if m.downtime < 0 {
		return errors.New("local: replacement downtime must be non-negative")
	}
	return nil
}

func (m *Manager) ownedContainer(ctx context.Context, id string) (containerInspect, error) {
	output, err := exec.CommandContext(ctx, m.runtime, "inspect", id).CombinedOutput()
	if err != nil {
		return containerInspect{}, fmt.Errorf("local: inspect container %s: %w: %s", id, err, strings.TrimSpace(string(output)))
	}
	var inspected []containerInspect
	if err := json.Unmarshal(output, &inspected); err != nil {
		return containerInspect{}, fmt.Errorf("local: parse container %s inspection: %w", id, err)
	}
	if len(inspected) != 1 {
		return containerInspect{}, fmt.Errorf("local: inspect container %s returned %d records", id, len(inspected))
	}
	if inspected[0].Config.Labels[clusterLabelKey] != m.cluster {
		return containerInspect{}, fmt.Errorf("local: container %s is not owned by cluster %s", id, m.cluster)
	}
	return inspected[0], nil
}

func (m *Manager) instanceFromInspect(inspected containerInspect) *instanceInfo {
	name := strings.TrimPrefix(inspected.Name, "/")
	privateIP := inspected.NetworkSettings.Networks[NetworkName(m.cluster)].IPAddress
	return &instanceInfo{
		runtime:   m.runtime,
		name:      name,
		privateIP: privateIP,
		state:     containerState(inspected.State),
	}
}

func containerState(state containerInspectState) compute.InstanceState {
	if state.Running {
		return compute.InstanceStateRunning
	}
	switch strings.ToLower(state.Status) {
	case "created", "configured", "restarting":
		return compute.InstanceStatePending
	case "dead", "removing":
		return compute.InstanceStateTerminated
	case "exited", "stopped", "paused":
		return compute.InstanceStateStopped
	default:
		return compute.InstanceStateUnknown
	}
}

type instanceInfo struct {
	runtime   string
	name      string
	privateIP string
	state     compute.InstanceState
}

func (i *instanceInfo) ID() string                   { return i.name }
func (i *instanceInfo) PublicIPv4() string           { return "" }
func (i *instanceInfo) PrivateIPv4() string          { return i.privateIP }
func (i *instanceInfo) State() compute.InstanceState { return i.state }
func (i *instanceInfo) RunCommand(ctx context.Context, command []string) (*compute.ExecuteResult, error) {
	return i.RunCommandWithOptions(ctx, command, nil)
}

func (i *instanceInfo) RunCommandWithOptions(ctx context.Context, command []string, opts *compute.RunCommandOptions) (*compute.ExecuteResult, error) {
	if len(command) == 0 {
		return nil, errors.New("local: command is required")
	}
	if opts != nil && opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	args := []string{"exec"}
	if opts != nil && opts.Stdin != nil {
		args = append(args, "--interactive")
	}
	if opts != nil && opts.WorkDir != "" {
		args = append(args, "--workdir", opts.WorkDir)
	}
	args = append(args, i.name)
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, i.runtime, args...)
	if opts != nil {
		cmd.Stdin = opts.Stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := &compute.ExecuteResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("local: exec in container %s: %w", i.name, err)
	}
	return result, nil
}

type containerInspect struct {
	ID     string `json:"Id"`
	Name   string
	Config struct {
		Image  string
		Cmd    []string
		Labels map[string]string
	}
	HostConfig struct {
		PortBindings map[string][]portBinding
	}
	NetworkSettings struct {
		Networks map[string]networkEndpoint
	}
	Mounts []containerMount
	State  containerInspectState
}

type containerInspectState struct {
	Status  string
	Running bool
}

type portBinding struct {
	HostIP   string
	HostPort string
}

type networkEndpoint struct {
	IPAddress string
}

type containerMount struct {
	Type        string
	Name        string
	Destination string
}
