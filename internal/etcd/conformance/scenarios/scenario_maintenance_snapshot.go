package scenarios

import (
	"fmt"
	"io"
	"os"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	bbolt "go.etcd.io/bbolt"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMaintenanceSnapshot tests the MaintenanceSnapshot scenario.
func RunMaintenanceSnapshot(runner Runner) {
	logutil.S().Infow("running", "scenario", MaintenanceSnapshot.String())

	result := &Result{
		Scenario:  MaintenanceSnapshot.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	prefix := runner.GenerateRandomKey(10)
	for i := range 3 {
		ctx, cancel := runner.NewCtx()
		_, putErr := cli.Put(ctx, fmt.Sprintf("%s/snap-%d", prefix, i), "value")
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %d: %v", i, putErr)

			return
		}
	}

	// Use very generous timeout for cloud/VPN environments where snapshot streaming
	// can have significant latency, especially for larger etcd databases.
	// Cross-datacenter WireGuard networks can be particularly slow for large transfers.
	// Use 3x the default timeout (3 * 90s = 270s = 4.5 minutes) for snapshots.
	snapshotTimeout := max(3*runner.DefaultTimeout(),
		// minimum 3 minutes for snapshots
		180*time.Second)
	ctx, cancel := runner.NewCtxTimeout(snapshotTimeout)
	snap, err := cli.Snapshot(ctx)
	if err != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("snapshot request failed: %v", err)

		return
	}
	defer func() { _ = snap.Close() }()

	// Use canonical etcd snapshots directory, falling back to temp for non-root users
	snapshotDir := "/var/lib/etcd/snapshots"
	if mkdirErr := os.MkdirAll(snapshotDir, 0o700); mkdirErr != nil {
		// Fall back to system temp directory for non-root users or tests
		snapshotDir = os.TempDir()
	}
	tmpFile, err := os.CreateTemp(snapshotDir, "etcd-infra-etcd-snapshot-*.db")
	if err != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to create temp file: %v", err)

		return
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	written, err := io.Copy(tmpFile, snap)
	cancel()
	if err != nil {
		_ = tmpFile.Close()
		result.Success = false
		result.Output = fmt.Sprintf("failed to write snapshot: %v", err)

		return
	}
	if written == 0 {
		_ = tmpFile.Close()
		result.Success = false
		result.Output = "snapshot stream was empty"

		return
	}
	if syncErr := tmpFile.Sync(); syncErr != nil {
		_ = tmpFile.Close()
		result.Success = false
		result.Output = fmt.Sprintf("failed to sync snapshot file: %v", syncErr)

		return
	}
	if closeErr := tmpFile.Close(); closeErr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to close snapshot file: %v", closeErr)

		return
	}

	db, err := bbolt.Open(tmpPath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("snapshot is not a valid bbolt file: %v", err)

		return
	}
	if err := db.View(func(_ *bbolt.Tx) error { return nil }); err != nil {
		_ = db.Close()
		result.Success = false
		result.Output = fmt.Sprintf("failed to read snapshot: %v", err)

		return
	}
	if err := db.Close(); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to close snapshot db: %v", err)

		return
	}

	result.Output = fmt.Sprintf("snapshot verified: %d bytes", written)
}
