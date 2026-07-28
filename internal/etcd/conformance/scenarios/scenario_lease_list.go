package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeaseList verifies Lease.Leases() returns all active leases.
// This is useful for monitoring and debugging TTL-based key distribution.
// ref. "clientv3/integration/TestLeaseList".
func RunLeaseList(runner Runner) {
	logutil.S().Infow("running", "scenario", LeaseList.String())

	result := &Result{
		Scenario:  LeaseList.String(),
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

	// Create multiple leases with long TTL to prevent expiration during test
	leaseIDs := make([]int64, 0, 3)
	for i := range 3 {
		ctx, cancel := runner.NewCtx()
		leaseResp, grantErr := cli.Grant(ctx, 300) // 300 second (5 min) TTL to ensure they don't expire
		cancel()
		if grantErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to grant lease %d: %v", i, grantErr)

			return
		}
		leaseIDs = append(leaseIDs, int64(leaseResp.ID))
	}

	// List leases and verify our new ones are included
	ctx, cancel := runner.NewCtx()
	listResp, err := cli.Lease.Leases(ctx)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to list leases: %v", err)

		return
	}

	// In a shared Kubernetes cluster, other leases may come and go, so we can't rely
	// on counting total leases. Instead, just verify our leases are in the response.
	if len(listResp.Leases) < 3 {
		result.Success = false
		result.Output = fmt.Sprintf("expected at least 3 leases, got %d", len(listResp.Leases))

		return
	}

	// Verify our leases are in the list
	foundLeases := make(map[int64]bool)
	for _, lease := range listResp.Leases {
		foundLeases[int64(lease.ID)] = true
	}

	for _, id := range leaseIDs {
		if !foundLeases[id] {
			result.Success = false
			result.Output = fmt.Sprintf("lease %d not found in lease list", id)

			return
		}
	}

	// Revoke one lease and verify it's removed from the list
	ctx, cancel = runner.NewCtx()
	_, err = cli.Revoke(ctx, clientv3.LeaseID(leaseIDs[0]))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to revoke lease: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	listResp, err = cli.Lease.Leases(ctx)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to list leases after revoke: %v", err)

		return
	}

	foundLeases = make(map[int64]bool)
	for _, lease := range listResp.Leases {
		foundLeases[int64(lease.ID)] = true
	}

	if foundLeases[leaseIDs[0]] {
		result.Success = false
		result.Output = fmt.Sprintf("revoked lease %d should not be in lease list", leaseIDs[0])

		return
	}

	// Clean up remaining leases
	for i := 1; i < len(leaseIDs); i++ {
		ctx, cancel = runner.NewCtx()
		_, _ = cli.Revoke(ctx, clientv3.LeaseID(leaseIDs[i]))
		cancel()
	}

	result.Output = fmt.Sprintf("created %d leases, verified listing and revocation", len(leaseIDs))
}
