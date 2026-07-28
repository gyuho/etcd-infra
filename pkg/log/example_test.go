//nolint:paralleltest // Uses shared global logger state.
package log_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/log"
)

func TestExample(t *testing.T) {
	require.NotPanics(t, func() {
		log.S().Info("hello")
	})
}
