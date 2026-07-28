package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMaintenanceHashKv tests the MaintenanceHashKv scenario.
func RunMaintenanceHashKv(runner Runner) {
	logutil.S().Infow("running", "scenario", MaintenanceHashKv.String())

	result := &Result{
		Scenario:  MaintenanceHashKv.String(),
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
	var rev int64
	for i := range 5 {
		ctx, cancel := runner.NewCtx()
		resp, putErr := cli.Put(ctx, fmt.Sprintf("%s/hash-%d", prefix, i), "value")
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %d: %v", i, putErr)

			return
		}
		rev = resp.Header.GetRevision()
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

	// Wait for all peers to reach the target revision before requesting HashKV.
	// On HA clusters, followers may lag behind the leader after the Put operations
	// complete on the cluster client. Without this wait, a follower may return
	// "required revision is a future revision" if it hasn't replicated the data yet.
	for idx, c := range perPeerClients {
		endpoints := c.Endpoints()
		if len(endpoints) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("client %d returned unexpected endpoints: %v", idx, endpoints)

			return
		}

		const maxRetries = 20
		var reached bool
		for attempt := range maxRetries {
			ctx, cancel := runner.NewCtx()
			statusResp, err := c.Status(ctx, endpoints[0])
			cancel()
			if err != nil {
				logutil.S().Warnw("status check failed, retrying",
					"endpoint", endpoints[0],
					"attempt", attempt+1,
					"error", err,
				)
				time.Sleep(200 * time.Millisecond)

				continue
			}
			if statusResp.Header.GetRevision() >= rev {
				reached = true

				break
			}
			logutil.S().Infow("peer not yet at target revision, waiting",
				"endpoint", endpoints[0],
				"peerRevision", statusResp.Header.GetRevision(),
				"targetRevision", rev,
				"attempt", attempt+1,
			)
			time.Sleep(200 * time.Millisecond)
		}
		if !reached {
			result.Success = false
			result.Output = fmt.Sprintf("peer %s did not reach revision %d after %d retries", endpoints[0], rev, maxRetries)

			return
		}
	}

	var hash uint32
	for idx, c := range perPeerClients {
		endpoints := c.Endpoints()
		if len(endpoints) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("client %d returned unexpected endpoints: %v", idx, endpoints)

			return
		}

		ctx, cancel := runner.NewCtx()
		resp, err := c.HashKV(ctx, endpoints[0], rev)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("hashkv failed for %s: %v", endpoints[0], err)

			return
		}
		if resp.Hash == 0 {
			result.Success = false
			result.Output = fmt.Sprintf("hashkv for %s returned empty hash", endpoints[0])

			return
		}
		if hash == 0 {
			hash = resp.Hash

			continue
		}
		if hash != resp.Hash {
			result.Success = false
			result.Output = fmt.Sprintf("hash mismatch for %s: expected %d, got %d", endpoints[0], hash, resp.Hash)

			return
		}
	}
}
