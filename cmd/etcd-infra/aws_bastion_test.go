package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// This file adapts the cluster state to the test fixture: direct VPC
// endpoints. The AWS e2e suites run on the cluster's stress client instances
// (via "aws drive"), which share the members' VPC and security groups, so
// member private IPs are directly reachable — no port-forwarding tunnels
// exist.

// awsE2EMemberEndpoints returns the members' direct VPC client endpoints.
// Requires the test to run in the members' VPC (on a stress client).
func awsE2EMemberEndpoints(t *testing.T, state awsState) []string {
	t.Helper()
	endpoints := make([]string, 0, len(state.Instances))
	for _, member := range state.Instances {
		require.NotEmpty(t, member.PrivateIPv4, "%s has no private IP", member.Name)
		endpoints = append(endpoints, "http://"+member.PrivateIPv4+":2379")
	}
	return endpoints
}
