package randutil

import (
	"math/rand"
	"sync"
	"time"
)

// Global random source.
var (
	//nolint:gochecknoglobals,gosec // Package-level random source; weak RNG is acceptable for general-purpose strings.
	rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	//nolint:gochecknoglobals // Protects rnd.
	mu sync.Mutex
)
