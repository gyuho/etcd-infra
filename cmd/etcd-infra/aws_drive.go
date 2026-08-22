package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"
)

// runAWSDrive executes a suite on the cluster's stress client instances. The
// binary ships to each client through S3, runs there against the VPC
// endpoints, writes its results on disk, and uploads them to S3; the driver
// downloads every client's results and prints a summary. No client traffic
// crosses a tunnel or a public path.
//
//	etcd-infra aws drive --name CLUSTER --binary ./etcd-infra-linux-amd64 //	  --bucket BUCKET --suite stress --args "--duration 60 --workers 10"
//
// For go-test suites, ship the compiled test binary and its flags:
//
//	etcd-infra aws drive --name CLUSTER --binary ./cmd.test //	  --bucket BUCKET --suite test --args "-test.run TestAWSReplace -test.v"
func runAWSDrive(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws drive", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	binary := flags.String("binary", "", "local linux/amd64 binary (or compiled test binary) to run on the stress clients")
	bucket := flags.String("bucket", "", "S3 bucket for binary distribution and result collection")
	suite := flags.String("suite", "stress", "suite to run: stress, conformance, or test")
	suiteArgs := flags.String("args", "", "arguments passed to the suite")
	env := flags.String("env", "", "comma-separated KEY=VALUE environment for the suite process")
	clients := flags.String("clients", "all", "stress clients to run on: all, or comma-separated 1-based indices")
	timeout := flags.Duration("timeout", 4*time.Hour, "per-client command timeout")
	resultsDir := flags.String("results-dir", "", "local directory for downloaded results (default: ./<cluster>-drive-results-<timestamp>)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	if *binary == "" || *bucket == "" {
		return errors.New("--binary and --bucket are required")
	}
	if *suite != "stress" && *suite != "conformance" && *suite != "test" {
		return fmt.Errorf("unknown suite %q (stress, conformance, or test)", *suite)
	}
	statePath, err := awsStatePath(*name)
	if err != nil {
		return err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		return err
	}
	if len(state.StressClients) == 0 {
		return fmt.Errorf("cluster %s has no stress clients; recreate it with 'etcd-infra aws up --stress-clients 1'", *name)
	}
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	manager := awsprovider.New(cfg)

	// Content-addressed binary key: re-uploading an unchanged build is cheap
	// to detect and safe to skip by hand.
	sum, err := fileSHA256(*binary)
	if err != nil {
		return err
	}
	binaryKey := fmt.Sprintf("etcd-infra/bin/%s/etcd-infra", sum[:16])
	if err := awsCLI(ctx, "", "s3", "cp", *binary, "s3://"+*bucket+"/"+binaryKey, "--region", state.Region); err != nil {
		return fmt.Errorf("upload driver binary: %w", err)
	}

	targets, err := selectStressClients(state, *clients)
	if err != nil {
		return err
	}
	endpoints := make([]string, 0, len(state.Instances))
	for _, instance := range state.Instances {
		endpoints = append(endpoints, "http://"+instance.PrivateIPv4+":2379")
	}
	stateJSON, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	runTag := fmt.Sprintf("%d", time.Now().Unix())

	fmt.Printf("driving %s on %d stress client(s): %s\n",
		*suite, len(targets), stressClientNames(state))
	var wg sync.WaitGroup
	results := make([]error, len(targets))
	for i, client := range targets {
		wg.Go(func() {
			script := driveScript(driveJob{
				Region:       state.Region,
				Bucket:       *bucket,
				BinaryKey:    binaryKey,
				Cluster:      state.Name,
				Client:       client.Name,
				RunTag:       runTag,
				Suite:        *suite,
				Args:         *suiteArgs,
				Env:          strings.Join(splitCSV(*env), " "),
				Endpoints:    strings.Join(endpoints, " "),
				EndpointsCSV: strings.Join(endpoints, ","),
				StateB64:     base64.StdEncoding.EncodeToString(stateJSON),
			})
			instance, getErr := manager.Get(ctx, client.ID)
			if getErr != nil {
				results[i] = fmt.Errorf("get %s: %w", client.Name, getErr)
				return
			}
			result, runErr := instance.RunCommandWithOptions(ctx,
				[]string{"bash", "-ceu", script},
				&compute.RunCommandOptions{Timeout: *timeout})
			if runErr != nil {
				results[i] = fmt.Errorf("run %s: %w", client.Name, runErr)
				return
			}
			if result.ExitCode != 0 {
				results[i] = fmt.Errorf("%s: suite exited %d: %s", client.Name, result.ExitCode, tailLines(result.Stderr, 5))
				return
			}
			fmt.Printf("%s: suite completed\n", client.Name)
		})
	}
	wg.Wait()

	dir := *resultsDir
	if dir == "" {
		dir = fmt.Sprintf("%s-drive-results-%s", state.Name, runTag)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	prefix := fmt.Sprintf("etcd-infra/results/%s/%s/", state.Name, runTag)
	if err := awsCLI(ctx, "", "s3", "sync", fmt.Sprintf("s3://%s/%s", *bucket, prefix), dir, "--region", state.Region); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: downloading results failed: %v\n", err)
	}
	printDriveSummary(dir, targets)

	var errs []error
	for _, err := range results {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Printf("results: %s\n", dir)
	return nil
}

type driveJob struct {
	Region    string
	Bucket    string
	BinaryKey string
	Cluster   string
	Client    string
	RunTag    string
	Suite     string
	Args      string
	Env       string // space-separated KEY=VALUE assignments for the suite process
	Endpoints string // space-separated member endpoints, for the metrics loop
	// EndpointsCSV is the comma-separated form the --endpoints flag expects.
	EndpointsCSV string
	StateB64     string
}

// driveScript renders the on-host driver: download the binary, snapshot the
// members' peer-sent counters, run the suite, snapshot again, and upload the
// results. The metrics come straight from the members' /metrics endpoint
// over the VPC — no tunnels, no public paths.
func driveScript(job driveJob) string {
	uploadPrefix := fmt.Sprintf("s3://%s/etcd-infra/results/%s/%s/%s", job.Bucket, job.Cluster, job.RunTag, job.Client)

	var runLine string
	switch job.Suite {
	case "test":
		// The shipped binary is the compiled test binary; args carry -test.run.
		runLine = fmt.Sprintf("ETCD_INFRA_AWS_E2E_STATE=\"$work/state.json\" %s ./etcd-infra %s", job.Env, job.Args)
	case "stress":
		runLine = fmt.Sprintf("%s ./etcd-infra stress --endpoints \"%s\" %s --results-file results.jsonl", job.Env, job.EndpointsCSV, job.Args)
	default:
		runLine = fmt.Sprintf("%s ./etcd-infra %s --endpoints \"%s\" %s", job.Env, job.Suite, job.EndpointsCSV, job.Args)
	}

	return "set -euo pipefail\n" +
		// Per-run work dir: overlapping drives on one client must not remove
		// each other's in-flight downloads.
		"work=/tmp/etcd-infra-drive-" + job.RunTag + "\n" +
		"rm -rf \"$work\"; mkdir -p \"$work\"; cd \"$work\"\n" +
		"export AWS_REGION=" + job.Region + "\n" +
		"ENDPOINTS=\"" + job.Endpoints + "\"\n" +
		"\n" +
		"aws s3 cp \"s3://" + job.Bucket + "/" + job.BinaryKey + "\" ./etcd-infra\n" +
		"chmod +x ./etcd-infra\n" +
		"echo \"" + job.StateB64 + "\" | base64 -d > state.json\n" +
		"\n" +
		"peer_bytes() {\n" +
		"  local total=0\n" +
		"  for ep in $ENDPOINTS; do\n" +
		"    local v\n" +
		"    v=$(curl -fsS --max-time 5 \"$ep/metrics\" 2>/dev/null | awk '$1 ~ /^etcd_network_peer_sent_bytes_total/ {s += $NF} END {printf \"%.0f\", s+0}') || v=0\n" +
		"    total=$((total + v))\n" +
		"  done\n" +
		"  echo \"$total\"\n" +
		"}\n" +
		"\n" +
		"peer_bytes > metrics-before.txt\n" +
		"set +e\n" +
		runLine + " > suite.log 2>&1\n" +
		"rc=$?\n" +
		"set -e\n" +
		"peer_bytes > metrics-after.txt\n" +
		"aws s3 cp results.jsonl \"" + uploadPrefix + "/results.jsonl\" 2>/dev/null || true\n" +
		"aws s3 cp metrics-before.txt \"" + uploadPrefix + "/metrics-before.txt\"\n" +
		"aws s3 cp metrics-after.txt \"" + uploadPrefix + "/metrics-after.txt\"\n" +
		"aws s3 cp suite.log \"" + uploadPrefix + "/suite.log\"\n" +
		// The state file can change on the client (e.g. a member replacement
		// records the new instance); ship it back so the driver stays in sync.
		"aws s3 cp state.json \"" + uploadPrefix + "/state.json\" 2>/dev/null || true\n" +
		"echo \"DRIVE_EXIT=$rc\"\n" +
		"exit $rc\n"
}

// selectStressClients resolves --clients (all or 1-based indices) against the
// recorded stress clients.
func selectStressClients(state awsState, sel string) ([]awsInstanceState, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" || sel == "all" {
		return state.StressClients, nil
	}
	var out []awsInstanceState
	for _, part := range strings.Split(sel, ",") {
		var idx int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &idx); err != nil {
			return nil, fmt.Errorf("invalid --clients entry %q", part)
		}
		if idx < 1 || idx > len(state.StressClients) {
			return nil, fmt.Errorf("--clients index %d out of range (%d stress clients)", idx, len(state.StressClients))
		}
		out = append(out, state.StressClients[idx-1])
	}
	return out, nil
}

// printDriveSummary prints one line per client: suite outcome and the
// peer-sent bytes the run consumed (summed across members).
func printDriveSummary(dir string, targets []awsInstanceState) {
	fmt.Println()
	fmt.Println("=== drive summary ===")
	for _, client := range targets {
		base := filepath.Join(dir, client.Name)
		before := readMetricFile(filepath.Join(base, "metrics-before.txt"))
		after := readMetricFile(filepath.Join(base, "metrics-after.txt"))
		peer := "?"
		if before >= 0 && after >= 0 {
			peer = fmt.Sprintf("%d", after-before)
		}
		outcome := "?"
		if data, err := os.ReadFile(filepath.Join(base, "results.jsonl")); err == nil {
			passed, failed := 0, 0
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(line, "{") {
					continue
				}
				if strings.Contains(line, "\"success\":true") {
					passed++
				} else if strings.Contains(line, "\"success\":false") {
					failed++
				}
			}
			outcome = fmt.Sprintf("scenarios %d passed / %d failed", passed, failed)
		}
		fmt.Printf("%-44s peer-sent-bytes %14s   %s\n", client.Name, peer, outcome)
	}
}

// readMetricFile reads a one-number metrics file; -1 when absent or invalid.
func readMetricFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	var v int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &v); err != nil {
		return -1
	}
	return v
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// fileSHA256 hashes a file for content-addressed S3 keys.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// awsCLI runs the AWS CLI for S3 transfers. The CLI, not the SDK: the
// credential chain (including the test user's session) already works there,
// and the suite scripts depend on the CLI anyway.
func awsCLI(ctx context.Context, stdin string, args ...string) error {
	cmd := exec.CommandContext(ctx, "aws", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, tailLines(string(out), 5))
	}
	return nil
}
