package testtime

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// SlowPathMultiplier scales fixed timeouts and latency thresholds for
// high-latency client paths. The suite's budgets are tuned for direct or
// VPN links; SSM port-forwarding through a bastion adds ~150ms per RTT plus
// proxy jitter, which under load lands marginal measurements just over them.
// Set ETCD_INFRA_SLOW_PATH_MULTIPLIER (e.g. "2") only on such paths. Values
// below 1 are rejected: the knob loosens budgets for slow paths, never
// tightens them.
var SlowPathMultiplier = sync.OnceValue(func() float64 {
	return parseSlowPathMultiplier(os.Getenv("ETCD_INFRA_SLOW_PATH_MULTIPLIER"))
})

// ScaleDuration multiplies a timeout by the slow-path multiplier.
func ScaleDuration(d time.Duration) time.Duration {
	return time.Duration(float64(d) * SlowPathMultiplier())
}

// ScaleLatency multiplies a latency threshold (in the same unit as the
// threshold) by the slow-path multiplier.
func ScaleLatency(threshold float64) float64 {
	return threshold * SlowPathMultiplier()
}

func parseSlowPathMultiplier(v string) float64 {
	if v == "" {
		return 1
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 1 {
		return 1
	}
	return f
}
