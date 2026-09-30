package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validAWSDevUpOptions() awsDevUpOptions {
	return awsDevUpOptions{
		Name:               "dev01",
		VPCID:              "vpc-1",
		Arch:               "amd64",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		VolumeSizeGB:       32,
		MountPoint:         "/mnt/data",
		Bucket:             "etcd-infra-e2e-123-us-west-2-v0-202609",
		UbuntuRelease:      "24.04",
	}
}

func TestValidateAWSDevUpOptions(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateAWSDevUpOptions(validAWSDevUpOptions()))

	for _, tc := range []struct {
		name   string
		mutate func(*awsDevUpOptions)
		want   string
	}{
		{"missing vpc", func(o *awsDevUpOptions) { o.VPCID = "" }, "--vpc is required"},
		{"missing profile", func(o *awsDevUpOptions) { o.IAMInstanceProfile = " " }, "--instance-profile is required"},
		{"missing bucket", func(o *awsDevUpOptions) { o.Bucket = "" }, "--bucket is required"},
		{"bad bucket", func(o *awsDevUpOptions) { o.Bucket = "Bad_Bucket" }, "not a valid S3 bucket"},
		{"bad arch", func(o *awsDevUpOptions) { o.Arch = "386" }, "--arch must be amd64 or arm64"},
		{"zero volume", func(o *awsDevUpOptions) { o.VolumeSizeGB = 0 }, "--volume-size-gb"},
		{"huge volume", func(o *awsDevUpOptions) { o.VolumeSizeGB = 16385 }, "--volume-size-gb"},
		{"relative mount", func(o *awsDevUpOptions) { o.MountPoint = "mnt/data" }, "--mount-point"},
		{"root mount", func(o *awsDevUpOptions) { o.MountPoint = "/" }, "--mount-point"},
		{"trailing slash", func(o *awsDevUpOptions) { o.MountPoint = "/mnt/data/" }, "--mount-point"},
		{"dotdot mount", func(o *awsDevUpOptions) { o.MountPoint = "/mnt/../etc" }, "--mount-point"},
		{"shell chars", func(o *awsDevUpOptions) { o.MountPoint = "/mnt/$(id)" }, "--mount-point"},
		// A blank volume over OS state would break snap/apt on every boot.
		{"system dir", func(o *awsDevUpOptions) { o.MountPoint = "/var" }, "must be /data"},
		{"nested system dir", func(o *awsDevUpOptions) { o.MountPoint = "/var/lib" }, "must be /data"},
		{"usr local", func(o *awsDevUpOptions) { o.MountPoint = "/usr/local" }, "must be /data"},
		{"bare mnt", func(o *awsDevUpOptions) { o.MountPoint = "/mnt" }, "must be /data"},
		{"bad name", func(o *awsDevUpOptions) { o.Name = "dev 01" }, "invalid cluster name"},
	} {
		opts := validAWSDevUpOptions()
		tc.mutate(&opts)
		require.ErrorContains(t, validateAWSDevUpOptions(opts), tc.want, tc.name)
	}

	for _, mountPoint := range []string{"/mnt/data", "/mnt/nvme.0/x", "/data", "/data/etcd", "/srv/bench", "/var/lib/etcd"} {
		opts := validAWSDevUpOptions()
		opts.MountPoint = mountPoint
		require.NoError(t, validateAWSDevUpOptions(opts), mountPoint)
	}
}

// The dry run is the default and must never reach AWS: no credentials exist
// in this test, so any AWS call would fail.
func TestAWSDevUpDryRunMakesNoAWSCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")

	err := runAWSDev(context.Background(), []string{
		"up", "--name", "dev01", "--vpc", "vpc-1",
		"--instance-profile", "etcd-infra-ssm", "--bucket", "etcd-infra-e2e-b",
	})
	require.NoError(t, err)

	path, err := awsStatePath("dev01")
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "dry run must not write state")

	err = runAWSDev(context.Background(), []string{
		"up", "--name", "dev01", "--vpc", "vpc-1",
		"--instance-profile", "etcd-infra-ssm", "--bucket", "etcd-infra-e2e-b", "--ubuntu-release", "23.04",
	})
	require.ErrorContains(t, err, "unsupported Ubuntu release")
}

func TestAWSDevCommandsRefuseClusterState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path, err := awsStatePath("prod")
	require.NoError(t, err)
	require.NoError(t, writeAWSState(path, awsState{
		Name: "prod", Region: "us-west-2",
		Instances: []awsInstanceState{{Name: "prod-1", ID: "i-1"}},
	}))

	for _, sub := range []string{"down", "status"} {
		err := runAWSDev(context.Background(), []string{sub, "--name", "prod"})
		require.ErrorContains(t, err, "not a dev box", sub)
	}
	err = runAWSDev(context.Background(), []string{"run", "--name", "prod", "--", "true"})
	require.ErrorContains(t, err, "not a dev box")
	_, err = os.Stat(path)
	require.NoError(t, err, "cluster state must be untouched")
}

func TestAWSDevDownWithoutStateIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, runAWSDev(context.Background(), []string{"down", "--name", "dev01"}))
}

func TestAWSDevConfirmPurged(t *testing.T) {
	t.Parallel()

	require.NoError(t, awsDevConfirmPurged("111111111111", "111111111111"))
	// A NotFound seen from another account may hide a live box: never
	// conclude it is gone (that would drop state and orphan it).
	require.ErrorContains(t, awsDevConfirmPurged("222222222222", "111111111111"), "created in account")
	require.Error(t, awsDevConfirmPurged("111111111111", ""))
}

func TestAWSDevRunArgumentValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01"}), "usage:")
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01", "--script", "x.sh", "--", "ls"}), "usage:")
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01", "--timeout", "0s", "--", "ls"}), "--timeout")
	require.ErrorContains(t, runAWSDev(ctx, []string{"bogus"}), "unknown aws dev command")
}

func testAWSDevState() awsState {
	return awsState{
		Name:      "dev01",
		Region:    "us-west-2",
		Instances: []awsInstanceState{{Name: "dev01", ID: "i-1"}},
		Dev: &awsDevState{
			MountPoint: "/mnt/data",
			Bucket:     "etcd-infra-e2e-b",
			ResultsURI: awsDevResultsURI("etcd-infra-e2e-b", "dev01"),
		},
	}
}

func TestAWSDevScriptsAreValidBash(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"setup":     awsDevSetupScript(testAWSDevState()),
		"user-data": awsDevUserData,
		"run":       awsDevRunScript("if true; then echo ok; fi"),
		"mount":     awsDataVolumeSetupScript("/var/lib/etcd"),
	} {
		out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
}

func TestAWSDevSetupScriptWiresMountAndResults(t *testing.T) {
	t.Parallel()

	script := awsDevSetupScript(testAWSDevState())
	assert.Contains(t, script, "mount_point=/mnt/data\n")
	assert.Contains(t, script, "export ETCD_INFRA_DEV_DIR=/mnt/data\n")
	assert.Contains(t, script, "export ETCD_INFRA_DEV_RESULTS=s3://etcd-infra-e2e-b/etcd-infra/dev/dev01/\n")
	assert.Contains(t, script, "export AWS_REGION=us-west-2\n")
	// The probe must write inside the results prefix the role policy allows.
	assert.Contains(t, script, `aws s3 cp - "${ETCD_INFRA_DEV_RESULTS}.dev-ready"`)
	// Mounting must come before anything writes to the mount point.
	assert.Less(t, strings.Index(script, "mount \"$mount_point\""), strings.Index(script, "aws --version"))
}

func TestAWSDevRunScriptFailsFastWithoutEnvironment(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(awsDevEnvFile); err == nil {
		t.Skip("host has a dev box environment file")
	}
	// Without the box environment the user command must not run at all.
	out, err := exec.Command("bash", "-c", awsDevRunScript("echo SHOULD-NOT-RUN")).CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 97, exitErr.ExitCode())
	assert.NotContains(t, string(out), "SHOULD-NOT-RUN")
	assert.Contains(t, string(out), "'aws dev up' did not finish")
}

func TestAWSDevRunScriptPropagatesExitCode(t *testing.T) {
	t.Parallel()

	// Replace the environment prelude with a real file to exercise the
	// wrapper end to end.
	dir := t.TempDir()
	env := dir + "/env.sh"
	require.NoError(t, os.WriteFile(env, []byte("export ETCD_INFRA_DEV_DIR="+dir+"\n"), 0o600))
	script := strings.Replace(awsDevRunScript("pwd; exit 7"), awsDevEnvFile, env, 1)
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 7, exitErr.ExitCode())
	resolved, err := os.Readlink(dir)
	if err != nil {
		resolved = dir
	}
	assert.Contains(t, []string{dir + "\n", resolved + "\n"}, strings.TrimPrefix(string(out), "/private"))
}
