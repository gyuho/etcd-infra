package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMaintenanceStatus tests the MaintenanceStatus scenario.
func RunMaintenanceStatus(runner Runner) {
	logutil.S().Infow("running", "scenario", MaintenanceStatus.String())

	result := &Result{
		Scenario:  MaintenanceStatus.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	perPeerClients, err := runner.NewPerPeerClients()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create per-peer clients: %v", err)

		return
	}
	if len(perPeerClients) == 0 {
		result.Success = false
		result.Output = noPeerClientsMsg

		return
	}
	defer func() {
		for _, cli := range perPeerClients {
			_ = cli.Close()
		}
	}()

	leadersObserved := 0

	for idx, cli := range perPeerClients {
		endpoints := cli.Endpoints()
		if len(endpoints) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("client %d returned unexpected endpoints: %v", idx, endpoints)

			return
		}
		ctx, cancel := runner.NewCtx()
		status, err := cli.Status(ctx, endpoints[0])
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status request failed for %s: %v", endpoints[0], err)

			return
		}

		if status.Version == "" {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status for %s returned empty version", endpoints[0])

			return
		}
		if status.DbSize == 0 {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status for %s reported zero db size", endpoints[0])

			return
		}
		if status.DbSizeInUse == 0 {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status for %s reported zero db size in use", endpoints[0])

			return
		}
		if status.RaftIndex < status.RaftAppliedIndex {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status for %s reported raft index %d < applied %d",
				endpoints[0], status.RaftIndex, status.RaftAppliedIndex)

			return
		}
		if status.Leader == 0 {
			result.Success = false
			result.Output = fmt.Sprintf("maintenance status for %s reported no leader", endpoints[0])

			return
		}
		if status.Header.GetMemberId() == status.Leader {
			leadersObserved++
		}
	}

	if leadersObserved == 0 {
		result.Success = false
		result.Output = "maintenance status did not report any self-leaders"
	}
}
