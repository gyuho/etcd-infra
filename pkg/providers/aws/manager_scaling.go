package aws

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"git.tbd/etcd-infra/pkg/providers/compute"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
)

// WaitForReady polls until the instance is running and SSM command execution
// is possible. Combines EC2 state polling with an SSM connectivity probe.
func (m *Manager) WaitForReady(ctx context.Context, id compute.InstanceHandle, timeout time.Duration) (compute.Instance, error) {
	if m.ec2 == nil {
		return nil, errors.New("aws: ec2 client is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("aws: instance id is required")
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		inst, err := m.Get(ctx, id)
		if err == nil && m.instanceIsReady(ctx, inst) {
			return inst, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("aws: instance %s not ready after %v", id, timeout)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aws: wait for instance %s ready: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// SetDesiredCapacity updates the min, desired, and max capacity of an ASG.
func (m *Manager) SetDesiredCapacity(ctx context.Context, group compute.GroupHandle, desired, minCapacity, maxCapacity int) error {
	if m.asg == nil {
		return fmt.Errorf("aws group-scaling (no ASG client): %w", compute.ErrNotSupported)
	}
	if strings.TrimSpace(group) == "" {
		return errors.New("aws: group handle is required")
	}
	// AWS ASG API requires int32; validate bounds to prevent overflow on 64-bit hosts.
	if minCapacity < 0 || desired < 0 || maxCapacity < 0 ||
		minCapacity > math.MaxInt32 || desired > math.MaxInt32 || maxCapacity > math.MaxInt32 {
		return errors.New("aws: capacity value out of valid int32 range")
	}

	_, err := m.asg.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(group),
		MinSize:              aws.Int32(int32(minCapacity)), //nolint:gosec // validated ≤ math.MaxInt32 above
		DesiredCapacity:      aws.Int32(int32(desired)),     //nolint:gosec // validated ≤ math.MaxInt32 above
		MaxSize:              aws.Int32(int32(maxCapacity)), //nolint:gosec // validated ≤ math.MaxInt32 above
	})
	if err != nil {
		return fmt.Errorf("aws: set ASG %s capacity (min=%d, desired=%d, max=%d): %w", group, minCapacity, desired, maxCapacity, err)
	}
	return nil
}

// WaitForDesiredCapacity polls the ASG until the expected number of instances
// are InService and Healthy, returning their instance IDs.
func (m *Manager) WaitForDesiredCapacity(ctx context.Context, group compute.GroupHandle, expected int, timeout time.Duration) ([]compute.InstanceHandle, error) {
	if m.asg == nil {
		return nil, fmt.Errorf("aws group-scaling (no ASG client): %w", compute.ErrNotSupported)
	}
	groupName := strings.TrimSpace(group)
	if groupName == "" {
		return nil, errors.New("aws: group handle is required")
	}
	if expected <= 0 {
		return nil, nil
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		ids := m.queryReadyInstances(ctx, groupName)
		if len(ids) >= expected {
			return ids[:expected], nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("aws: timed out waiting for %d instances in ASG %s", expected, groupName)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aws: wait for ASG %s capacity: %w", groupName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// WaitForZeroCapacity polls the ASG until all instances are terminated.
func (m *Manager) WaitForZeroCapacity(ctx context.Context, group compute.GroupHandle, timeout time.Duration) error {
	if m.asg == nil {
		return fmt.Errorf("aws group-scaling (no ASG client): %w", compute.ErrNotSupported)
	}
	groupName := strings.TrimSpace(group)
	if groupName == "" {
		return errors.New("aws: group handle is required")
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		out, err := m.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []string{groupName},
		})
		if err == nil && len(out.AutoScalingGroups) > 0 && len(out.AutoScalingGroups[0].Instances) == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("aws: timed out waiting for zero capacity in ASG %s", groupName)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("aws: wait for ASG %s zero capacity: %w", groupName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// instanceIsReady returns true when inst is running and SSM is reachable (if configured).
func (m *Manager) instanceIsReady(ctx context.Context, inst compute.Instance) bool {
	if inst.State() != compute.InstanceStateRunning {
		return false
	}
	// No SSM client; running state is sufficient.
	if m.ssm == nil {
		return true
	}
	// Probe SSM connectivity; instance is reachable only when
	// the SSM agent is running and the IAM instance profile is attached.
	_, err := inst.RunCommand(ctx, []string{"echo", "ready"})
	return err == nil
}

// queryReadyInstances describes the ASG and returns InService+Healthy instance IDs,
// sorted for deterministic output. Returns nil on API error or empty group.
func (m *Manager) queryReadyInstances(ctx context.Context, groupName string) []compute.InstanceHandle {
	out, err := m.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{groupName},
	})
	if err != nil || len(out.AutoScalingGroups) == 0 {
		return nil
	}
	ids := make([]compute.InstanceHandle, 0, len(out.AutoScalingGroups[0].Instances))
	for _, inst := range out.AutoScalingGroups[0].Instances {
		id := strings.TrimSpace(aws.ToString(inst.InstanceId))
		if id == "" {
			continue
		}
		lifecycle := strings.ToLower(string(inst.LifecycleState))
		health := strings.ToLower(strings.TrimSpace(aws.ToString(inst.HealthStatus)))
		if lifecycle != "" && lifecycle != "inservice" {
			continue
		}
		if health != "" && health != "healthy" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
