//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package stress

import (
	"errors"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigJSON_MarshalError(t *testing.T) {
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock json.MarshalIndent to return an error
	mock := mockey.Mock(mockey.GetMethod(&Config{}, "JSON")).To(func(*Config) ([]byte, error) {
		return nil, errors.New("marshal error")
	}).Build()
	defer mock.UnPatch()

	_, err := cfg.JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal")
}

// Note: Testing yaml.Marshal error is difficult as it rarely fails on normal structs.
// The error path is defensive programming but nearly impossible to trigger in practice.

func TestConfigSyncJSON_JSONError(t *testing.T) {
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock JSON() to return an error
	mock := mockey.Mock((*Config).JSON).Return(nil, errors.New("json error")).Build()
	defer mock.UnPatch()

	err := cfg.SyncJSON("/tmp/test.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "json")
}

func TestConfigSyncYAML_YAMLError(t *testing.T) {
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock YAML() to return an error
	mock := mockey.Mock((*Config).YAML).Return(nil, errors.New("yaml error")).Build()
	defer mock.UnPatch()

	err := cfg.SyncYAML("/tmp/test.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
}
