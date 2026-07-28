package randutil_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rand "git.tbd/etcd-infra/pkg/randutil"
)

func TestWord(t *testing.T) {
	t.Parallel()

	// Test sequential calls.
	w1 := rand.Word()
	w2 := rand.Word()
	require.NotEmpty(t, w1)
	require.NotEmpty(t, w2)
	require.NotEqual(t, w1, w2, "Sequential calls should return different words")

	// Test concurrent calls.
	const numGoroutines = 100
	wordsSeen := sync.Map{}
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for range numGoroutines {
		go func() {
			defer wg.Done()
			w := rand.Word()
			_, loaded := wordsSeen.LoadOrStore(w, true)
			// Use assert in goroutines to avoid calling runtime.Goexit via require.FailNow
			assert.False(t, loaded, "Concurrent calls should return different words (within the pool size)")
		}()
	}
	wg.Wait()
}
