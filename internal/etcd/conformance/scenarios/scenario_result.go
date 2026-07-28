package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"gopkg.in/yaml.v3"
)

// Result defines a test result for each scenario.
type Result struct {
	// Scenario is the test scenario name.
	Scenario string `json:"scenario"`

	// TimeStart is the timestamp when the model run started.
	TimeStart testtime.Time `json:"timeStart"`
	// TimeEnd is the timestamp when the model run finished.
	TimeEnd testtime.Time `json:"timeEnd"`
	// Took is the scenario test duration.
	Took testtime.Duration `json:"took"`

	// Success is 'true' if the test is successful.
	Success bool `json:"success"`

	// Output records additional test outputs.
	Output string `json:"output,omitempty"`
}

// RecordTimeEnd records the time end.
func (rs *Result) RecordTimeEnd(t testtime.Time) {
	rs.TimeEnd = t
	rs.Took = testtime.Duration{Duration: rs.TimeEnd.Sub(rs.TimeStart.Time)}
}

// Results is an ordered list of conformance scenario outcomes.
type Results []Result

// Failed returns true if any scenario did not succeed.
func (rs Results) Failed() bool {
	for _, r := range rs {
		if !r.Success {
			return true
		}
	}

	return false
}

// JSON serializes results to JSON bytes.
func (rs Results) JSON() ([]byte, error) {
	data, err := json.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal results: %w", err)
	}
	return data, nil
}

// SyncJSON writes results as JSON to the given file path.
func (rs Results) SyncJSON(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), 0o750); mkErr != nil {
			return fmt.Errorf("failed to create directory for %s: %w", file, mkErr)
		}
	}
	data, err := rs.JSON()
	if err != nil {
		return fmt.Errorf("failed to marshal results to JSON: %w", err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return fmt.Errorf("failed to write JSON file %s: %w", file, err)
	}

	return nil
}

// LoadResultsJSON reads and parses a JSON results file.
func LoadResultsJSON(file string) (Results, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file %s: %w", file, err)
	}

	return ParseResultsJSON(data)
}

// ParseResultsJSON unmarshals JSON bytes into Results.
func ParseResultsJSON(data []byte) (Results, error) {
	var rs Results
	err := json.Unmarshal(data, &rs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON results: %w", err)
	}

	return rs, nil
}

// YAML serializes results to YAML bytes.
func (rs Results) YAML() ([]byte, error) {
	data, err := yaml.Marshal(rs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal results: %w", err)
	}
	return data, nil
}

// SyncYAML writes results as YAML to the given file path.
func (rs Results) SyncYAML(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), 0o750); mkErr != nil {
			return fmt.Errorf("failed to create directory for %s: %w", file, mkErr)
		}
	}
	data, err := rs.YAML()
	if err != nil {
		return fmt.Errorf("failed to marshal results to YAML: %w", err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return fmt.Errorf("failed to write YAML file %s: %w", file, err)
	}

	return nil
}

// LoadResultsYAML reads and parses a YAML results file.
func LoadResultsYAML(file string) (Results, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file %s: %w", file, err)
	}

	return ParseResultsYAML(data)
}

// ParseResultsYAML unmarshals YAML bytes into Results.
func ParseResultsYAML(data []byte) (Results, error) {
	var rs Results
	err := yaml.Unmarshal(data, &rs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML results: %w", err)
	}

	return rs, nil
}
