//nolint:all // Coverage-oriented mockey tests.
package conformance

import (
	"errors"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

func TestRunSuccess(t *testing.T) {
	mockey.PatchConvey("Run success path", t, func() {
		mockey.Mock((*Config).ValidateAndSetDefaults).Return(nil).Build()
		mockey.Mock((*Config).RunScenarios).Return(scenarios.Results{
			{Scenario: "test-1", Success: true},
		}, nil).Build()

		err := Run(Options{Endpoints: []string{"http://localhost:2379"}})
		require.NoError(t, err)
	})
}

func TestRunWithFailedScenarios(t *testing.T) {
	mockey.PatchConvey("Run with some failed scenarios", t, func() {
		mockey.Mock((*Config).ValidateAndSetDefaults).Return(nil).Build()
		mockey.Mock((*Config).RunScenarios).Return(scenarios.Results{
			{Scenario: "test-1", Success: true},
			{Scenario: "test-2", Success: false, Output: "something broke"},
		}, nil).Build()

		err := Run(Options{Endpoints: []string{"http://localhost:2379"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conformance scenarios failed: 1")
	})
}

func TestRunRunScenariosError(t *testing.T) {
	mockey.PatchConvey("Run RunScenarios error", t, func() {
		mockey.Mock((*Config).ValidateAndSetDefaults).Return(nil).Build()
		mockey.Mock((*Config).RunScenarios).Return(nil, errors.New("etcd connect error")).Build()

		err := Run(Options{Endpoints: []string{"http://localhost:2379"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conformance tests failed")
	})
}
