package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file adapts the production tunnel core (aws_tunnel.go) to the test
// fixture: require-style failures and t.Cleanup lifecycle.

// startAWSBastionTunnels opens one SSM port-forwarding session per member
// through the cluster's bastion and returns the loopback client endpoints to
// use in place of the members' direct URLs. The sessions are IAM-
// authenticated (the caller's AWS credentials) and terminate at the bastion,
// which relays to each member's private IPv4 on TCP 2379. Requires the AWS
// CLI and session-manager-plugin on the test host.
func startAWSBastionTunnels(t *testing.T, state awsState) []string {
	t.Helper()
	require.NotNil(t, state.Bastion, "cluster %s has no bastion in its state", state.Name)

	endpoints := make([]string, 0, len(state.Instances))
	for _, member := range state.Instances {
		ctx, cancel := context.WithCancel(context.Background())
		endpoint, stop, err := startAWSSSMPortForward(ctx, state.Region, state.Bastion.ID, member.PrivateIPv4, 2379)
		require.NoError(t, err, "bastion tunnel to %s (%s)", member.Name, member.PrivateIPv4)
		t.Cleanup(func() {
			stop()
			cancel()
		})
		t.Logf("bastion tunnel to %s (%s): %s", member.Name, member.PrivateIPv4, endpoint)
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}
