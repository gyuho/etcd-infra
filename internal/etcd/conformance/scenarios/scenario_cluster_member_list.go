package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunClusterMemberList tests the ClusterMemberList scenario.
func RunClusterMemberList(runner Runner) {
	logutil.S().Infow("running", "scenario", ClusterMemberList.String())

	result := &Result{
		Scenario:  ClusterMemberList.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	// Use per-peer clients to determine the expected member count, then close them.
	perPeerClients, err := runner.NewPerPeerClients()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create per-peer clients: %v", err)

		return
	}
	if len(perPeerClients) == 0 {
		result.Success = false
		result.Output = "no per-peer clients returned"

		return
	}
	expectedMemberCount := len(perPeerClients)
	for _, cli := range perPeerClients {
		_ = cli.Close()
	}

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create aggregate client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := runner.NewCtx()
	resp, err := cli.MemberList(ctx)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("member list request failed: %v", err)

		return
	}

	if len(resp.Members) != expectedMemberCount {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d members, got %d", expectedMemberCount, len(resp.Members))

		return
	}

	seenIDs := make(map[uint64]struct{}, len(resp.Members))
	for _, member := range resp.Members {
		if member.ID == 0 {
			result.Success = false
			result.Output = "member list returned a zero member ID"

			return
		}
		if _, exists := seenIDs[member.ID]; exists {
			result.Success = false
			result.Output = fmt.Sprintf("member list returned duplicate member ID %d", member.ID)

			return
		}
		seenIDs[member.ID] = struct{}{}

		if len(member.ClientURLs) == 0 {
			result.Success = false
			result.Output = fmt.Sprintf("member %d returned no client URLs", member.ID)

			return
		}

		// Note: we do NOT match member-advertised client URLs against connection
		// endpoints because etcd members advertise their localhost URL
		// (e.g. https://127.0.0.1:2379) while bastion-based testing connects
		// via ENI private IPs. The structural checks above (non-zero ID, unique
		// IDs, non-empty client URLs, correct member count) are sufficient.
	}
}
