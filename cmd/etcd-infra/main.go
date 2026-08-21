package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/coreos/go-semver/semver"

	"git.tbd/etcd-infra/internal/etcd/conformance"
	"git.tbd/etcd-infra/internal/etcd/installer"
	"git.tbd/etcd-infra/internal/etcd/stress"
)

const defaultEndpoint = "http://127.0.0.1:2379"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: etcd-infra <install|local|aws|stress|conformance|metrics>")
	}

	switch args[0] {
	case "install":
		return runInstall(ctx, args[1:])
	case "local":
		return runLocal(ctx, args[1:])
	case "aws":
		return runAWS(ctx, args[1:])
	case "stress":
		return runStress(args[1:])
	case "conformance":
		return runConformance(args[1:])
	case "metrics":
		return runMetrics(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runInstall(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	version := flags.String("version", "latest", "etcd release version")
	dir := flags.String("dir", "bin", "installation directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dir) == "" {
		return errors.New("install directory is required")
	}

	resolvedVersion, err := resolveVersion(ctx, *version)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}

	downloads := []struct {
		name string
		fn   func(context.Context, string, ...installer.OpOption) ([]byte, error)
	}{
		{name: "etcd", fn: installer.DownloadEtcd},
		{name: "etcdctl", fn: installer.DownloadEtcdctl},
		{name: "etcdutl", fn: installer.DownloadEtcdutl},
	}
	for _, download := range downloads {
		target := filepath.Join(*dir, download.name)
		if _, err := download.fn(
			ctx,
			target,
			installer.WithVersion(resolvedVersion),
			installer.WithVersionCheck(true),
		); err != nil {
			return fmt.Errorf("install %s: %w", download.name, err)
		}
	}

	absoluteDir, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("resolve install directory: %w", err)
	}
	fmt.Printf("installed etcd %s in %s\n", releaseTag(resolvedVersion), absoluteDir)
	return nil
}

func runStress(args []string) error {
	flags := flag.NewFlagSet("stress", flag.ContinueOnError)
	endpoints := flags.String("endpoints", defaultEndpoint, "comma-separated etcd endpoints")
	caCert := flags.String("ca-cert", "", "path to CA certificate")
	clientCert := flags.String("client-cert", "", "path to client certificate")
	clientKey := flags.String("client-key", "", "path to client key")
	testKeyPrefix := flags.String("test-key-prefix", stress.DefaultTestKeyPrefix, "key prefix for test data")
	scenario := flags.String("scenario", "", "specific scenario ID to run (default: all)")
	stepTimeout := flags.String("stress-step-timeout", "", "override per-scenario step timeout (Go duration)")
	duration := flags.Int("duration", 60, "duration in seconds")
	workers := flags.Int("workers", 10, "concurrent workers")
	rps := flags.Int("rps", 100, "requests per second; 0 is unlimited")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *duration < 1 || *workers < 1 || *rps < 0 {
		return errors.New("duration and workers must be positive; rps must be non-negative")
	}

	return stress.Run(stress.Options{
		Endpoints:      splitEndpoints(*endpoints),
		CACertFile:     *caCert,
		ClientCertFile: *clientCert,
		ClientKeyFile:  *clientKey,
		TestKeyPrefix:  *testKeyPrefix,
		ScenarioID:     *scenario,
		StepTimeout:    *stepTimeout,
		Duration:       *duration,
		Workers:        *workers,
		RequestsPerSec: *rps,
	})
}

func runConformance(args []string) error {
	flags := flag.NewFlagSet("conformance", flag.ContinueOnError)
	endpoints := flags.String("endpoints", defaultEndpoint, "comma-separated etcd endpoints")
	caCert := flags.String("ca-cert", "", "path to CA certificate")
	clientCert := flags.String("client-cert", "", "path to client certificate")
	clientKey := flags.String("client-key", "", "path to client key")
	testKeyPrefix := flags.String("test-key-prefix", conformance.DefaultTestKeyPrefix, "key prefix for test data")
	scenario := flags.String("scenario", "", "specific scenario ID to run (default: all)")
	stepTimeout := flags.String("conformance-step-timeout", "", "override per-scenario step timeout (Go duration)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	return conformance.Run(conformance.Options{
		Endpoints:      splitEndpoints(*endpoints),
		CACertFile:     *caCert,
		ClientCertFile: *clientCert,
		ClientKeyFile:  *clientKey,
		TestKeyPrefix:  *testKeyPrefix,
		ScenarioID:     *scenario,
		StepTimeout:    *stepTimeout,
	})
}

func resolveVersion(ctx context.Context, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", errors.New("etcd version is required")
	}
	if version == "latest" {
		latest, err := installer.LatestVersion(ctx)
		if err != nil {
			return "", err
		}
		version = latest
	}
	version = strings.TrimPrefix(version, "v")
	if _, err := semver.NewVersion(version); err != nil {
		return "", fmt.Errorf("invalid etcd version %q: %w", version, err)
	}
	return version, nil
}

func releaseTag(version string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func splitEndpoints(value string) []string {
	return splitCSV(value)
}

func splitCSV(value string) []string {
	fields := strings.Split(strings.TrimSpace(value), ",")
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			values = append(values, value)
		}
	}
	return values
}
