package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMaintenanceDefragment tests the MaintenanceDefragment scenario.
func RunMaintenanceDefragment(runner Runner) {
	logutil.S().Infow("running", "scenario", MaintenanceDefragment.String())

	result := &Result{
		Scenario:  MaintenanceDefragment.String(),
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
	for i := range 5 {
		ctx, cancel := runner.NewCtx()
		_, putErr := cli.Put(ctx, fmt.Sprintf("%s/defrag-%d", prefix, i), "value")
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %d: %v", i, putErr)

			return
		}
	}

	perPeerClients, err := runner.NewPerPeerClients()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create per-peer clients: %v", err)

		return
	}
	defer func() {
		for _, c := range perPeerClients {
			_ = c.Close()
		}
	}()

	for idx, c := range perPeerClients {
		endpoints := c.Endpoints()
		if len(endpoints) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("client %d returned unexpected endpoints: %v", idx, endpoints)

			return
		}

		ctx, cancel := runner.NewCtx()
		_, err := c.Defragment(ctx, endpoints[0])
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("defragment failed for %s: %v", endpoints[0], err)

			return
		}
	}
}
