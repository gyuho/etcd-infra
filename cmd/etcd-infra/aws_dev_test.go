package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
)

func validAWSDevUpOptions() awsDevUpOptions {
	return awsDevUpOptions{
		Name:               "dev01",
		Arch:               "amd64",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		VolumeSizeGB:       32,
		MountPoint:         "/mnt/data",
		Bucket:             "etcd-infra-e2e-123-us-west-2-v0-202609",
		UbuntuRelease:      "24.04",
		Count:              1,
	}
}

func TestValidateAWSDevUpOptions(t *testing.T) {
	t.Parallel()

	// No VPC is fine: it resolves to $ETCD_INFRA_AWS_VPC or the default VPC.
	require.NoError(t, validateAWSDevUpOptions(validAWSDevUpOptions()))

	for _, tc := range []struct {
		name   string
		mutate func(*awsDevUpOptions)
		want   string
	}{
		{"zero count", func(o *awsDevUpOptions) { o.Count = 0 }, "--count must be between 1 and 20"},
		{"huge count", func(o *awsDevUpOptions) { o.Count = 21 }, "--count must be between 1 and 20"},
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
		// Launch template names need at least 3 characters.
		{"short name", func(o *awsDevUpOptions) { o.Name = "d1" }, "at least 3 characters"},
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

// awsTestNoCredentials makes any AWS call fail fast instead of finding real
// credentials on the host.
func awsTestNoCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-west-2")
	for _, key := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(key, "")
	}
}

// The dry run is the default and must never reach AWS.
func TestAWSDevUpDryRunMakesNoAWSCalls(t *testing.T) {
	awsTestNoCredentials(t)

	err := runAWSDev(context.Background(), []string{
		"up", "--name", "dev01", "--count", "3",
		"--instance-profile", "etcd-infra-ssm", "--bucket", "etcd-infra-e2e-b",
	})
	require.NoError(t, err)

	path, err := awsStatePath("dev01")
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "dry run must not write state")

	err = runAWSDev(context.Background(), []string{
		"up", "--name", "dev01", "--instance-profile", "etcd-infra-ssm", "--bucket", "etcd-infra-e2e-b", "--ubuntu-release", "23.04",
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

	for _, args := range [][]string{
		{"down", "--name", "prod"},
		{"status", "--name", "prod"},
		{"scale", "--name", "prod", "--count", "2"},
		{"run", "--name", "prod", "--", "true"},
		{"etcd", "--name", "prod", "--version", "v3.6.0"},
	} {
		require.ErrorContains(t, runAWSDev(context.Background(), args), "not a dev group", args[0])
	}
	_, err = os.Stat(path)
	require.NoError(t, err, "cluster state must be untouched")
}

func TestAWSDevDownWithoutStateIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, runAWSDev(context.Background(), []string{"down", "--name", "dev01"}))
}

// "aws down" on a dev group must go through the group teardown (terminating
// members one by one would only make the group relaunch them), and without
// a verifiable account it must keep the state rather than drop it.
func TestAWSDownOnDevGroupUsesGroupTeardown(t *testing.T) {
	awsTestNoCredentials(t)

	state := testAWSDevState()
	path, err := awsStatePath(state.Name)
	require.NoError(t, err)
	require.NoError(t, writeAWSState(path, state))

	err = runAWS(context.Background(), []string{"down", "--name", state.Name})
	require.ErrorContains(t, err, "identify AWS account")
	require.ErrorContains(t, err, "state kept")
	_, err = os.Stat(path)
	require.NoError(t, err, "state must survive a teardown that could not verify anything")
}

func TestReadAWSDevStateRejectsIncompleteState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	state := testAWSDevState()
	state.Dev.AutoScalingGroup = ""
	path, err := awsStatePath(state.Name)
	require.NoError(t, err)
	require.NoError(t, writeAWSState(path, state))
	_, _, err = readAWSDevState(state.Name)
	require.ErrorContains(t, err, "invalid dev group state")
}

// State from the single-instance release (commit 64f3eb8) must stay
// deletable, or its box and volume would bill forever; everything else
// must point the user at "down".
func TestAWSDevLegacyStateIsTeardownOnly(t *testing.T) {
	awsTestNoCredentials(t)

	legacy := awsState{
		Name: "dev01", Region: "us-west-2",
		Instances: []awsInstanceState{{Name: "dev01", ID: "i-legacy"}},
		Dev:       &awsDevState{AccountID: "111111111111", ResultsURI: awsDevResultsURI("etcd-infra-e2e-b", "dev01")},
	}
	path, err := awsStatePath(legacy.Name)
	require.NoError(t, err)
	require.NoError(t, writeAWSState(path, legacy))

	state, _, err := readAWSDevState(legacy.Name)
	require.NoError(t, err)
	assert.True(t, awsDevIsLegacy(state))
	for _, args := range [][]string{
		{"status", "--name", "dev01"},
		{"scale", "--name", "dev01", "--count", "2"},
		{"run", "--name", "dev01", "--", "true"},
	} {
		require.ErrorContains(t, runAWSDev(context.Background(), args), "single-instance dev box", args[0])
	}
	// Down reaches AWS (here: fails to identify the account) and keeps state.
	err = runAWSDev(context.Background(), []string{"down", "--name", "dev01"})
	require.ErrorContains(t, err, "identify AWS account")
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestAWSDevArgumentValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01"}), "usage:")
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01", "--script", "x.sh", "--", "ls"}), "usage:")
	require.ErrorContains(t, runAWSDev(ctx, []string{"run", "--name", "dev01", "--timeout", "0s", "--", "ls"}), "--timeout")
	require.ErrorContains(t, runAWSDev(ctx, []string{"scale", "--name", "dev01"}), "--count must be between 0 and 20")
	require.ErrorContains(t, runAWSDev(ctx, []string{"scale", "--name", "dev01", "--count", "21"}), "--count must be between 0 and 20")
	require.ErrorContains(t, runAWSDev(ctx, []string{"bogus"}), "unknown aws dev command")
}

func TestAWSDevRunTargets(t *testing.T) {
	t.Parallel()

	members := []awsprovider.GroupMember{
		{ID: "i-1", LifecycleState: "InService"},
		{ID: "i-2", LifecycleState: "Pending"},
		{ID: "i-3", LifecycleState: "InService"},
	}
	got, err := awsDevRunTargets(members, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"i-1", "i-3"}, got, "default is every in-service box")

	got, err = awsDevRunTargets(members, []string{"i-3", "i-3"})
	require.NoError(t, err)
	assert.Equal(t, []string{"i-3"}, got)

	_, err = awsDevRunTargets(members, []string{"i-2"})
	require.ErrorContains(t, err, "not an in-service box")
	_, err = awsDevRunTargets(members, []string{"i-other"})
	require.ErrorContains(t, err, "not an in-service box")
	_, err = awsDevRunTargets(nil, nil)
	require.ErrorContains(t, err, "no in-service boxes")
}

func testAWSDevState() awsState {
	return awsState{
		Name:   "dev01",
		Region: "us-west-2",
		Dev: &awsDevState{
			AccountID:        "111111111111",
			MountPoint:       "/mnt/data",
			Bucket:           "etcd-infra-e2e-b",
			ResultsURI:       awsDevResultsURI("etcd-infra-e2e-b", "dev01"),
			Count:            2,
			AutoScalingGroup: "dev01",
			LaunchTemplate:   "dev01",
			OwnerToken:       "0123456789abcdef0123456789abcdef",
		},
	}
}

func TestAWSDevScriptsAreValidBash(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"user-data":  awsDevUserData(testAWSDevState()),
		"setup-wait": awsDevSetupWaitScript(),
		"run":        awsDevRunScript("if true; then echo ok; fi"),
		"mount":      awsDataVolumeSetupScript("/var/lib/etcd"),
	} {
		out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
}

func TestAWSDevUserDataWiring(t *testing.T) {
	t.Parallel()

	script := awsDevUserData(testAWSDevState())
	require.LessOrEqual(t, len(script), 16*1024, "EC2 caps user data at 16 KiB")
	assert.True(t, strings.HasPrefix(script, "#!/bin/bash\n"), "cloud-init runs user data by its shebang")
	assert.Contains(t, script, "mount_point=/mnt/data\n")
	// Each box writes its own prefix, so boxes never clobber each other.
	assert.Contains(t, script, `results=s3://etcd-infra-e2e-b/etcd-infra/dev/dev01/"${instance_id}/"`)
	assert.Contains(t, script, `aws s3 cp - "${results}.dev-ready"`)
	assert.Contains(t, script, "export ETCD_INFRA_DEV_RESULTS=${results}\n")
	// The exit code is recorded however the script ends.
	assert.Contains(t, script, "trap '")
	// Order: SSM agent, mount, CLI, S3 probe, then the environment file,
	// whose presence tells "run" that setup succeeded.
	order := []string{"amazon-ssm-agent --classic", `mount "$mount_point"`, "aws --version", ".dev-ready", "mv " + awsDevEnvFile + ".tmp " + awsDevEnvFile}
	for i := 1; i < len(order); i++ {
		assert.Less(t, strings.Index(script, order[i-1]), strings.Index(script, order[i]), "%q before %q", order[i-1], order[i])
	}
}

// The environment file is written by an unquoted heredoc: box values must
// expand at boot and $PATH must stay literal for login shells.
func TestAWSDevUserDataEnvironmentFile(t *testing.T) {
	t.Parallel()

	script := awsDevUserData(testAWSDevState())
	start := strings.Index(script, "cat > "+awsDevEnvFile)
	end := strings.Index(script, "\nEOF\n")
	require.Positive(t, start)
	require.Greater(t, end, start)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.sh")
	heredoc := strings.ReplaceAll(script[start:end+len("\nEOF\n")], awsDevEnvFile, envFile)
	cmd := exec.Command("bash", "-c", `instance_id=i-0abc; results="s3://b/etcd-infra/dev/dev01/${instance_id}/"; `+heredoc)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	// The heredoc writes <env>.tmp; setup renames it into place afterwards.
	env, err := exec.Command("bash", "-c", `PATH=/usr/bin:/bin; . `+envFile+`.tmp; echo "$ETCD_INFRA_DEV_INSTANCE_ID|$ETCD_INFRA_DEV_RESULTS|$ETCD_INFRA_DEV_DIR|$AWS_REGION|$PATH"`).CombinedOutput()
	require.NoError(t, err, string(env))
	assert.Equal(t, "i-0abc|s3://b/etcd-infra/dev/dev01/i-0abc/|/mnt/data|us-west-2|/usr/bin:/bin:/snap/bin\n", string(env))
}

func TestAWSDevSetupWaitScript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		code     string
		wantExit int
		wantOut  string
	}{
		{"0", 0, ""},
		{"3", 1, "setup failed (exit 3)"},
	} {
		dir := t.TempDir()
		exitFile, logFile := filepath.Join(dir, "exit"), filepath.Join(dir, "log")
		require.NoError(t, os.WriteFile(exitFile, []byte(tc.code+"\n"), 0o600))
		require.NoError(t, os.WriteFile(logFile, []byte("snap install failed\n"), 0o600))
		script := strings.NewReplacer(awsDevSetupExitFile, exitFile, awsDevSetupLog, logFile).Replace(awsDevSetupWaitScript())
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if tc.wantExit == 0 {
			require.NoError(t, err, string(out))
			continue
		}
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, tc.wantExit, exitErr.ExitCode())
		assert.Contains(t, string(out), tc.wantOut)
		assert.Contains(t, string(out), "snap install failed", "the log tail explains the failure")
	}
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
	assert.Contains(t, string(out), "first-boot setup has not finished or failed")
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
