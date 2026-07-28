package aws

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"git.tbd/etcd-infra/pkg/providers/compute"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Get retrieves an existing AWS instance by ID.
func (m *Manager) Get(ctx context.Context, id string) (compute.Instance, error) {
	if m.ec2 == nil {
		return nil, errors.New("aws: ec2 client is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("aws: instance id is required")
	}

	out, err := m.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{id},
	})
	if err != nil {
		return nil, fmt.Errorf("aws: describe instance %s: %w", id, err)
	}

	//nolint:gocritic // Struct copy acceptable for readability
	for _, reservation := range out.Reservations {
		//nolint:gocritic // Struct copy acceptable for readability
		for _, instance := range reservation.Instances {
			if aws.ToString(instance.InstanceId) != id {
				continue
			}
			return m.instanceFromEC2(instance), nil
		}
	}
	return nil, fmt.Errorf("aws: instance %s not found", id)
}

// List returns all non-terminated EC2 instances visible to current credentials.
// Handles paginated DescribeInstances responses to avoid silently truncating results.
func (m *Manager) List(ctx context.Context) ([]compute.Instance, error) {
	if m.ec2 == nil {
		return nil, errors.New("aws: ec2 client is nil")
	}

	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name: aws.String("instance-state-name"),
				Values: []string{
					string(types.InstanceStateNamePending),
					string(types.InstanceStateNameRunning),
					string(types.InstanceStateNameStopping),
					string(types.InstanceStateNameStopped),
				},
			},
		},
	}

	var instances []compute.Instance
	for {
		out, err := m.ec2.DescribeInstances(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("aws: list instances: %w", err)
		}

		//nolint:gocritic // Struct copy acceptable for readability
		for _, reservation := range out.Reservations {
			//nolint:gocritic // Struct copy acceptable for readability
			for _, instance := range reservation.Instances {
				if instance.InstanceId == nil || *instance.InstanceId == "" {
					continue
				}
				instances = append(instances, m.instanceFromEC2(instance))
			}
		}

		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		input.NextToken = out.NextToken
	}

	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID() < instances[j].ID()
	})
	return instances, nil
}
