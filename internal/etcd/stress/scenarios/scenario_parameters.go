package scenarios

import "time"

// Helpers aligning with conformance/common.go for scenario configuration.
func scenarioDuration(cfg StressConfig) time.Duration {
	if cfg.DurationSeconds <= 0 {
		return 60 * time.Second
	}

	return time.Duration(cfg.DurationSeconds) * time.Second
}

func workerCount(cfg StressConfig) int {
	if cfg.ConcurrentWorkers <= 0 {
		return 1
	}

	return cfg.ConcurrentWorkers
}

func valueSize(cfg StressConfig, fallback int) int {
	if cfg.ValueSizeBytes <= 0 {
		return fallback
	}

	return cfg.ValueSizeBytes
}

func keySize(cfg StressConfig, fallback int) int {
	if cfg.KeySizeBytes <= 0 {
		return fallback
	}

	return cfg.KeySizeBytes
}

func intMax(a, b int) int {
	if a > b {
		return a
	}

	return b
}
