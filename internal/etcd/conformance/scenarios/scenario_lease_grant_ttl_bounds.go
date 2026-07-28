package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeaseGrantTTLBounds verifies lease TTL bounds are enforced correctly.
// Kubernetes lease_manager.go expects predictable TTL behavior within etcd's supported range.
// ref. "clientv3/integration/TestLeaseGrant".
func RunLeaseGrantTTLBounds(runner Runner) {
	logutil.S().Infow("running", "scenario", LeaseGrantTTLBounds.String())

	result := &Result{
		Scenario:  LeaseGrantTTLBounds.String(),
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

	// Test 1: Minimum TTL (1 second - minimum allowed)
	ctx, cancel := runner.NewCtx()
	leaseResp, err := cli.Grant(ctx, 1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant 1s TTL: %v", err)

		return
	}
	if leaseResp.TTL < 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected TTL >= 1, got %d", leaseResp.TTL)

		return
	}
	minLeaseID := leaseResp.ID

	// Test 2: Normal TTL range (60 seconds - common for Kubernetes)
	ctx, cancel = runner.NewCtx()
	leaseResp, err = cli.Grant(ctx, 60)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant 60s TTL: %v", err)

		return
	}
	if leaseResp.TTL != 60 {
		result.Success = false
		result.Output = fmt.Sprintf("expected TTL 60, got %d", leaseResp.TTL)

		return
	}
	normalLeaseID := leaseResp.ID

	// Test 3: Large TTL (3600 seconds = 1 hour)
	ctx, cancel = runner.NewCtx()
	leaseResp, err = cli.Grant(ctx, 3600)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant 3600s TTL: %v", err)

		return
	}
	if leaseResp.TTL != 3600 {
		result.Success = false
		result.Output = fmt.Sprintf("expected TTL 3600, got %d", leaseResp.TTL)

		return
	}
	largeLeaseID := leaseResp.ID

	// Test 4: Very large TTL (86400 seconds = 24 hours)
	ctx, cancel = runner.NewCtx()
	leaseResp, err = cli.Grant(ctx, 86400)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant 86400s TTL: %v", err)

		return
	}
	if leaseResp.TTL != 86400 {
		result.Success = false
		result.Output = fmt.Sprintf("expected TTL 86400, got %d", leaseResp.TTL)

		return
	}
	veryLargeLeaseID := leaseResp.ID

	// Test 5: TTL of 0 should be rejected or auto-adjusted
	// Note: etcd may treat TTL 0 differently - some versions auto-adjust to minimum
	ctx, cancel = runner.NewCtx()
	leaseResp, err = cli.Grant(ctx, 0)
	cancel()
	// Either should error or adjust to minimum TTL
	if err == nil {
		// If no error, TTL should be adjusted to minimum
		if leaseResp.TTL < 1 {
			result.Success = false
			result.Output = fmt.Sprintf("TTL 0 should be adjusted to minimum, got %d", leaseResp.TTL)

			return
		}
		// Clean up
		ctx, cancel = runner.NewCtx()
		_, _ = cli.Revoke(ctx, leaseResp.ID)
		cancel()
	}
	// If error, that's also acceptable behavior

	// Test 6: Verify TimeToLive returns correct remaining TTL
	ctx, cancel = runner.NewCtx()
	ttlResp, err := cli.TimeToLive(ctx, normalLeaseID)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get TimeToLive: %v", err)

		return
	}
	// TTL should be <= granted TTL and > 0
	if ttlResp.TTL <= 0 || ttlResp.TTL > 60 {
		result.Success = false
		result.Output = fmt.Sprintf("expected TTL in range (0, 60], got %d", ttlResp.TTL)

		return
	}
	if ttlResp.GrantedTTL != 60 {
		result.Success = false
		result.Output = fmt.Sprintf("expected GrantedTTL 60, got %d", ttlResp.GrantedTTL)

		return
	}

	// Clean up leases
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, minLeaseID)
	cancel()
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, normalLeaseID)
	cancel()
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, largeLeaseID)
	cancel()
	ctx, cancel = runner.NewCtx()
	_, _ = cli.Revoke(ctx, veryLargeLeaseID)
	cancel()

	result.Output = "TTL bounds verified: 1s (min), 60s (normal), 3600s (1hr), 86400s (24hr)"
}
