package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"gopkg.in/yaml.v3"
)

// Result mirrors conformance Result structure exactly.
type Result struct {
	// Same fields as conformance Result
	Scenario  string            `json:"scenario"`
	TimeStart testtime.Time     `json:"timeStart"`
	TimeEnd   testtime.Time     `json:"timeEnd"`
	Took      testtime.Duration `json:"took"`
	Success   bool              `json:"success"`
	Output    string            `json:"output,omitempty"`

	// Stress-specific additions with explicit units (cognitive load principle)
	TotalRequests      int64 `json:"totalRequests"`
	SuccessfulRequests int64 `json:"successfulRequests"`
	FailedRequests     int64 `json:"failedRequests"`

	// Latency metrics with clear units
	AverageLatency testtime.Duration `json:"averageLatency"`
	P50Latency     testtime.Duration `json:"p50Latency"`
	P95Latency     testtime.Duration `json:"p95Latency"`
	P99Latency     testtime.Duration `json:"p99Latency"`
	MaxLatency     testtime.Duration `json:"maxLatency"`

	// LatencyBuckets is the mergeable latency histogram (bucket i covers
	// [0.0625*2^(i/8), 0.0625*2^((i+1)/8)) ms; see metrics.go). Counts from
	// separate runs sum into a fleet-wide distribution for exact aggregated
	// percentiles.
	LatencyBuckets []int64 `json:"latencyBuckets,omitempty" yaml:"latencyBuckets,omitempty"`

	// Throughput with clear units
	RequestsPerSecond float64 `json:"requestsPerSecond"`

	// Data transfer metrics
	BytesWritten int64 `json:"bytesWritten"`
	BytesRead    int64 `json:"bytesRead"`
}

// RecordTimeEnd mirrors the conformance helper for computing duration and RPS.
func (rs *Result) RecordTimeEnd(t testtime.Time) {
	rs.TimeEnd = t
	rs.Took = testtime.Duration{Duration: rs.TimeEnd.Sub(rs.TimeStart.Time)}

	// Calculate requests per second if we have the data
	if rs.Took.Duration > 0 && rs.TotalRequests > 0 {
		rs.RequestsPerSecond = float64(rs.TotalRequests) / rs.Took.Seconds()
	}
}

// StressResults is an ordered list of stress test outcomes with latency and throughput metrics.
type StressResults []Result

// Failed returns true if any stress scenario did not succeed.
func (rs StressResults) Failed() bool {
	for i := range rs {
		if !rs[i].Success {
			return true
		}
	}

	return false
}

// TotalRequests returns the total number of requests across all stress runs.
func (rs StressResults) TotalRequests() int64 {
	var total int64
	for i := range rs {
		total += rs[i].TotalRequests
	}

	return total
}

// SuccessRate returns the ratio of successful requests to total requests across all runs.
func (rs StressResults) SuccessRate() float64 {
	totalReqs := rs.TotalRequests()
	if totalReqs == 0 {
		return 0
	}

	var successReqs int64
	for i := range rs {
		successReqs += rs[i].SuccessfulRequests
	}

	return float64(successReqs) / float64(totalReqs)
}

// AverageLatency returns the mean latency in milliseconds across all stress runs.
func (rs StressResults) AverageLatency() float64 {
	if len(rs) == 0 {
		return 0
	}

	var total float64
	var count int
	for i := range rs {
		if rs[i].AverageLatency.Duration > 0 {
			total += float64(rs[i].AverageLatency.Milliseconds())
			count++
		}
	}

	if count == 0 {
		return 0
	}

	return total / float64(count)
}

// JSON methods - exactly like conformance.
func (rs StressResults) JSON() ([]byte, error) {
	data, err := json.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stress results: %w", err)
	}
	return data, nil
}

// SyncJSON writes stress results as JSON to the given file path.
func (rs StressResults) SyncJSON(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), 0o750); mkErr != nil {
			return fmt.Errorf("failed to create directory: %w", mkErr)
		}
	}
	data, err := rs.JSON()
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	err = os.WriteFile(file, data, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}
	return nil
}

// LoadStressResultsJSON reads and parses a JSON stress results file.
func LoadStressResultsJSON(file string) (StressResults, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %w", err)
	}

	return ParseStressResultsJSON(data)
}

// ParseStressResultsJSON unmarshals JSON bytes into StressResults.
func ParseStressResultsJSON(data []byte) (StressResults, error) {
	var rs StressResults
	err := json.Unmarshal(data, &rs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return rs, nil
}

// YAML methods - exactly like conformance.
func (rs StressResults) YAML() ([]byte, error) {
	data, err := yaml.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal stress results: %w", err)
	}
	return data, nil
}

// SyncYAML writes stress results as YAML to the given file path.
func (rs StressResults) SyncYAML(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), 0o750); mkErr != nil {
			return fmt.Errorf("failed to create directory: %w", mkErr)
		}
	}
	data, err := rs.YAML()
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	err = os.WriteFile(file, data, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write YAML file: %w", err)
	}
	return nil
}

// LoadStressResultsYAML reads and parses a YAML stress results file.
func LoadStressResultsYAML(file string) (StressResults, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file: %w", err)
	}

	return ParseStressResultsYAML(data)
}

// ParseStressResultsYAML unmarshals YAML bytes into StressResults.
func ParseStressResultsYAML(data []byte) (StressResults, error) {
	var rs StressResults
	err := yaml.Unmarshal(data, &rs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	return rs, nil
}
