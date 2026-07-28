package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAWSUpOptions(t *testing.T) {
	t.Parallel()

	opts := awsUpOptions{
		Name:               "etcd-infra",
		VPCID:              "vpc-1",
		AMI:                "ami-1",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		Arch:               "amd64",
		Members:            3,
	}
	require.NoError(t, validateAWSUpOptions(opts))

	opts.IAMInstanceProfile = ""
	require.ErrorContains(t, validateAWSUpOptions(opts), "instance profile is required")
	opts.IAMInstanceProfile = "etcd-infra-ssm"
	opts.Arch = "386"
	require.ErrorContains(t, validateAWSUpOptions(opts), "arch must be amd64 or arm64")
}

func TestAWSBootstrapScriptUsesClusterTopologyAndChecksums(t *testing.T) {
	t.Parallel()

	state := awsState{
		Name:    "etcd-infra",
		Version: "3.7.1",
		Instances: []awsInstanceState{
			{Name: "etcd-infra-1", PrivateIPv4: "10.0.0.1"},
			{Name: "etcd-infra-2", PrivateIPv4: "10.0.0.2"},
			{Name: "etcd-infra-3", PrivateIPv4: "10.0.0.3"},
		},
	}
	members := awsMembers(state)
	script := awsBootstrapScript(members[0], members, state.Name, state.Version, "amd64")

	assert.Contains(t, script, "etcd-v3.7.1-linux-amd64.tar.gz")
	assert.Contains(t, script, "SHA256SUMS")
	assert.Contains(t, script, "--initial-cluster")
	assert.Contains(t, script, "etcd-infra-3=http://10.0.0.3:2380")
	assert.Contains(t, script, "Type=simple")
	assert.Contains(t, script, "systemctl enable --now etcd-infra.service")
	assert.NotContains(t, script, "kube")
}

func TestAWSStateRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	want := awsState{
		Name:    "etcd-infra",
		Region:  "us-west-2",
		Version: "3.7.1",
		Instances: []awsInstanceState{
			{Name: "etcd-infra-1", ID: "i-1", PrivateIPv4: "10.0.0.1"},
		},
	}
	require.NoError(t, writeAWSState(path, want))
	got, err := readAWSState(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
