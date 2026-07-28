package httputil

import (
	"time"
)

// Op represents HTTP operation options.
type Op struct {
	timeout          time.Duration
	downloadFilePath string
	userAgent        string
}

// OpOption is a functional option for configuring HTTP operations.
type OpOption func(*Op)

func (op *Op) applyOpts(opts []OpOption) {
	for _, opt := range opts {
		opt(op)
	}
}

// WithTimeout returns an OpOption that sets the timeout for HTTP operations.
func WithTimeout(dur time.Duration) OpOption {
	return func(op *Op) {
		op.timeout = dur
	}
}

// WithDownloadFilePath returns an OpOption that sets the download file path.
func WithDownloadFilePath(path string) OpOption {
	return func(op *Op) {
		op.downloadFilePath = path
	}
}

// WithUserAgent returns an OpOption that sets the User-Agent header.
func WithUserAgent(agent string) OpOption {
	return func(op *Op) {
		op.userAgent = agent
	}
}
