// Package httptestutil wraps the standard library httptest package with IPv4-only
// server constructors for sandboxed test environments that reject IPv6 binds.
package httptestutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	stdhttptest "net/http/httptest"
)

type (
	// Server aliases the standard library test server type.
	Server = stdhttptest.Server
	// ResponseRecorder aliases the standard library response recorder type.
	ResponseRecorder = stdhttptest.ResponseRecorder
)

// NewRecorder proxies net/http/httptest.NewRecorder.
func NewRecorder() *ResponseRecorder {
	return stdhttptest.NewRecorder()
}

// NewRequest proxies net/http/httptest.NewRequest.
func NewRequest(method, target string, body io.Reader) *http.Request {
	return stdhttptest.NewRequest(method, target, body)
}

// NewRequestWithContext proxies net/http/httptest.NewRequestWithContext.
func NewRequestWithContext(ctx context.Context, method, target string, body io.Reader) *http.Request {
	return stdhttptest.NewRequestWithContext(ctx, method, target, body)
}

// NewServer creates an HTTP test server bound to an IPv4 loopback listener.
func NewServer(handler http.Handler) *Server {
	srv := NewUnstartedServer(handler)
	srv.Start()
	return srv
}

// NewTLSServer creates a TLS test server bound to an IPv4 loopback listener.
func NewTLSServer(handler http.Handler) *Server {
	srv := NewUnstartedServer(handler)
	srv.StartTLS()
	return srv
}

// NewUnstartedServer creates an unstarted test server bound to an IPv4 loopback listener.
func NewUnstartedServer(handler http.Handler) *Server {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("httptest: failed to listen on a tcp4 port: %v", err))
	}

	return &stdhttptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
}
