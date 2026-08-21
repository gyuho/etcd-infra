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
	script := awsBootstrapScript(members[0], members, state.Name, awsBootstrapOptions{Version: state.Version, Arch: "amd64"})

	assert.Contains(t, script, "etcd-v3.7.1-linux-amd64.tar.gz")
	assert.Contains(t, script, "SHA256SUMS")
	assert.Contains(t, script, "--initial-cluster")
	assert.Contains(t, script, "etcd-infra-3=http://10.0.0.3:2380")
	assert.Contains(t, script, "Type=simple")
	assert.Contains(t, script, "systemctl enable --now etcd-infra.service")
	assert.NotContains(t, script, "kube")
	assert.NotContains(t, script, "Environment=")
}

func TestAWSBootstrapScriptCustomBinary(t *testing.T) {
	t.Parallel()

	state := awsState{
		Name:    "etcd-infra",
		Version: "latest",
		Instances: []awsInstanceState{
			{Name: "etcd-infra-1", PrivateIPv4: "10.0.0.1"},
			{Name: "etcd-infra-2", PrivateIPv4: "10.0.0.2"},
			{Name: "etcd-infra-3", PrivateIPv4: "10.0.0.3"},
		},
	}
	members := awsMembers(state)
	script := awsBootstrapScript(members[2], members, state.Name, awsBootstrapOptions{
		BinaryURL:    "https://example-bucket.s3.us-west-2.amazonaws.com/etcd-fix",
		BinarySHA256: "abc123",
		ExtraArgs:    []string{"--snapshot-count=10", "--snapshot-catchup-entries=10"},
		Env:          []string{"GOFAIL_HTTP=127.0.0.1:2234", `GOFAIL_FAILPOINTS=snapDBDirSyncError=return("injected snap dir fsync failure")`},
	})

	assert.Contains(t, script, "binary=https://example-bucket.s3.us-west-2.amazonaws.com/etcd-fix")
	assert.Contains(t, script, "binary_sha256=abc123")
	assert.Contains(t, script, "sha256sum -c checksum")
	assert.NotContains(t, script, "SHA256SUMS")
	assert.NotContains(t, script, "etcdctl")
	assert.Contains(t, script, "--snapshot-count=10 --snapshot-catchup-entries=10")
	assert.Contains(t, script, `Environment="GOFAIL_HTTP=127.0.0.1:2234"`)
	assert.Contains(t, script, `Environment="GOFAIL_FAILPOINTS=snapDBDirSyncError=return(\"injected snap dir fsync failure\")"`)
	assert.Contains(t, script, "name etcd-infra-3")
}

func TestValidateAWSUpOptionsBinaryAndEnv(t *testing.T) {
	t.Parallel()

	base := awsUpOptions{
		Name:               "etcd-infra",
		VPCID:              "vpc-1",
		AMI:                "ami-1",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		Arch:               "amd64",
		Members:            3,
	}

	opts := base
	opts.BinaryURL = "https://example.com/etcd"
	require.ErrorContains(t, validateAWSUpOptions(opts), "must be set together")
	opts.BinarySHA256 = "abc123"
	require.NoError(t, validateAWSUpOptions(opts))

	opts.BinaryURL = "http://insecure.example.com/etcd"
	require.ErrorContains(t, validateAWSUpOptions(opts), "must be an https URL")
	opts.BinaryURL = "https://example.com/etcd"

	opts.Env = "GOFAIL_HTTP=127.0.0.1:2234"
	require.NoError(t, validateAWSUpOptions(opts))
	opts.Env = "MISSING_EQUALS"
	require.ErrorContains(t, validateAWSUpOptions(opts), "invalid --env entry")
}

func TestAWSGofailDropInKeepsHTTPEndpoint(t *testing.T) {
	t.Parallel()

	// The empty Environment= reset in the drop-in must not lose GOFAIL_HTTP:
	// systemd clears the whole environment list, and T2's failpoint clear
	// goes through the gofail HTTP endpoint.
	dropIn := awsE2EGofailDropIn(`GOFAIL_FAILPOINTS=snapDBDirSyncError=return("injected snap dir fsync failure")`)
	assert.Contains(t, dropIn, "Environment=\n")
	assert.Contains(t, dropIn, `Environment="GOFAIL_HTTP=127.0.0.1:2234"`)
	assert.Contains(t, dropIn, `Environment="GOFAIL_FAILPOINTS=snapDBDirSyncError=return(\"injected snap dir fsync failure\")"`)
}

func TestAWSHealthCurlScript(t *testing.T) {
	t.Parallel()

	script := awsHealthCurlScript([]string{"http://10.0.0.1:2379", "http://10.0.0.2:2379"})
	assert.Contains(t, script, "curl -fsS http://10.0.0.1:2379/health")
	assert.Contains(t, script, "curl -fsS http://10.0.0.2:2379/health")
	assert.Contains(t, script, `"health":"true"`)
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
		Bastion: &awsInstanceState{Name: "etcd-infra-bastion", ID: "i-bastion", PrivateIPv4: "10.0.0.9"},
	}
	require.NoError(t, writeAWSState(path, want))
	got, err := readAWSState(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestAWSStateRoundTripBastionOnly(t *testing.T) {
	t.Parallel()

	// A partial "aws down" failure can leave a state with zero members and
	// only the bastion; it must stay readable so the retry can finish.
	path := filepath.Join(t.TempDir(), "state.json")
	want := awsState{
		Name:    "etcd-infra",
		Region:  "us-west-1",
		Version: "3.7.1",
		Bastion: &awsInstanceState{Name: "etcd-infra-bastion", ID: "i-bastion", PrivateIPv4: "10.0.0.9"},
	}
	require.NoError(t, writeAWSState(path, want))
	got, err := readAWSState(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestValidateAWSUpOptionsBastionTypeRequiresBastion(t *testing.T) {
	t.Parallel()

	opts := awsUpOptions{
		Name:               "etcd-infra",
		VPCID:              "vpc-1",
		AMI:                "ami-1",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		Arch:               "amd64",
		Members:            3,
		BastionType:        "t3a.micro",
	}
	require.ErrorContains(t, validateAWSUpOptions(opts), "--bastion-instance-type requires --bastion")
	opts.Bastion = true
	require.NoError(t, validateAWSUpOptions(opts))
}

func TestDefaultBastionInstanceType(t *testing.T) {
	t.Parallel()

	// The relay shuttles low-rate TCP streams only; the nano tier matches
	// that load and must stay on the AMI's architecture.
	assert.Equal(t, "t3a.nano", defaultBastionInstanceType("amd64"))
	assert.Equal(t, "t4g.nano", defaultBastionInstanceType("arm64"))
}
