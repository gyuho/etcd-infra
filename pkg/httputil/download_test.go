package httputil

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	httptest "git.tbd/etcd-infra/pkg/testutil/httptest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownload(t *testing.T) {
	t.Parallel()

	//nolint:gosec // G404: math/rand is sufficient for test data generation
	d := fmt.Sprintf("%x", rand.Int63())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, d)
	}))
	defer ts.Close()

	contentLength, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err, "HeadURLForContentLength failed")
	assert.Equal(t, int64(len(d)), contentLength, "HeadURLForContentLength content length mismatch")

	data, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err, "DownloadURL failed")
	assert.Equal(t, d, string(data), "DownloadURL content mismatch")

	filePath, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err, "DownloadURLToTmpFile failed")
	defer func() { _ = os.Remove(filePath) }()

	fileContents, err := os.ReadFile(filePath)
	require.NoError(t, err, "ReadFile failed")
	assert.Equal(t, d, string(fileContents), "DownloadURLToTmpFile content mismatch")
}

func TestDownloadURL_NotFound(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloadURL_InternalServerError(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestDownloadURLToFile_WithCustomPath(t *testing.T) {
	t.Parallel()

	content := "custom path content"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, content)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom_download.txt")

	filePath, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second), WithDownloadFilePath(customPath))
	require.NoError(t, err)
	assert.Equal(t, customPath, filePath)

	fileContents, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, content, string(fileContents))
}

func TestDownloadURLToFile_NotFound(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestHeadURLForContentLength_MissingHeader(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Don't set Content-Length header
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Content-Length header is missing")
}

func TestHeadURLForContentLength_NotFound(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestDownloadURL_InvalidURL(t *testing.T) {
	t.Parallel()

	_, err := DownloadURL(context.Background(), "http://invalid-host-that-does-not-exist.local:99999", WithTimeout(100*time.Millisecond))
	require.Error(t, err)
}

func TestDownloadURL_LargeContent(t *testing.T) {
	t.Parallel()

	// Generate 1MB of content
	largeContent := make([]byte, 1024*1024)
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(largeContent)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(largeContent)
	}))
	defer ts.Close()

	data, err := DownloadURL(context.Background(), ts.URL, WithTimeout(30*time.Second))
	require.NoError(t, err)
	assert.Len(t, data, len(largeContent))
	assert.Equal(t, largeContent, data)
}

func TestOpOptions(t *testing.T) {
	t.Parallel()

	t.Run("WithTimeout", func(t *testing.T) {
		t.Parallel()
		op := &Op{}
		opt := WithTimeout(10 * time.Second)
		opt(op)
		assert.Equal(t, 10*time.Second, op.timeout)
	})

	t.Run("WithDownloadFilePath", func(t *testing.T) {
		t.Parallel()
		op := &Op{}
		opt := WithDownloadFilePath("/tmp/test.txt")
		opt(op)
		assert.Equal(t, "/tmp/test.txt", op.downloadFilePath)
	})

	t.Run("WithUserAgent", func(t *testing.T) {
		t.Parallel()
		op := &Op{}
		opt := WithUserAgent("etcd-infra-test")
		opt(op)
		assert.Equal(t, "etcd-infra-test", op.userAgent)
	})

	t.Run("applyOpts", func(t *testing.T) {
		t.Parallel()
		op := &Op{}
		op.applyOpts([]OpOption{
			WithTimeout(5 * time.Second),
			WithDownloadFilePath("/custom/path.txt"),
			WithUserAgent("etcd-infra-test"),
		})
		assert.Equal(t, 5*time.Second, op.timeout)
		assert.Equal(t, "/custom/path.txt", op.downloadFilePath)
		assert.Equal(t, "etcd-infra-test", op.userAgent)
	})
}

func TestNewRequestWithUserAgent(t *testing.T) {
	t.Parallel()

	op := &Op{}
	op.applyOpts([]OpOption{WithUserAgent("etcd-infra-test")})

	req, err := newRequest(context.Background(), http.MethodGet, "http://example.com", op)
	require.NoError(t, err)
	assert.Equal(t, "etcd-infra-test", req.Header.Get("User-Agent"))
}

func TestNewRequest_NoUserAgent(t *testing.T) {
	t.Parallel()

	// Test that User-Agent is not set when not provided
	op := &Op{}

	req, err := newRequest(context.Background(), http.MethodGet, "http://example.com", op)
	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("User-Agent"))
}

func TestNewRequest_InvalidURL(t *testing.T) {
	t.Parallel()

	// Test with an invalid URL that will fail http.NewRequest
	op := &Op{}

	// A URL with an invalid control character will fail parsing
	_, err := newRequest(context.Background(), http.MethodGet, "http://example.com/\x00", op)
	require.Error(t, err)
}

func TestHeadURLForContentLength_InvalidContentLength(t *testing.T) {
	t.Parallel()

	// Test with a non-integer Content-Length header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "not-a-number")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.Error(t, err)
}

func TestDownloadURLToFile_InvalidDestPath(t *testing.T) {
	t.Parallel()

	content := "test content"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, content)
	}))
	defer ts.Close()

	// Try to download to a path that doesn't exist and can't be created
	// (using a path inside a non-existent directory)
	_, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second), WithDownloadFilePath("/nonexistent/dir/file.txt"))
	require.Error(t, err)
}

func TestDownloadURL_EmptyContent(t *testing.T) {
	t.Parallel()

	// Test downloading empty content
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write nothing
	}))
	defer ts.Close()

	data, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestDownloadURLToFile_EmptyContent(t *testing.T) {
	t.Parallel()

	// Test downloading empty content to file
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	filePath, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err)
	defer func() { _ = os.Remove(filePath) }()

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Empty(t, content)
}

func TestDownloadURL_WithUserAgent(t *testing.T) {
	t.Parallel()

	// Test that User-Agent is properly sent in the request
	var receivedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()

	_, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second), WithUserAgent("etcd-infra-test-agent"))
	require.NoError(t, err)
	assert.Equal(t, "etcd-infra-test-agent", receivedUA)
}

func TestHeadURLForContentLength_WithUserAgent(t *testing.T) {
	t.Parallel()

	// Test that User-Agent is properly sent in HEAD request
	var receivedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second), WithUserAgent("etcd-infra-head-test"))
	require.NoError(t, err)
	assert.Equal(t, "etcd-infra-head-test", receivedUA)
}

func TestHeadURLForContentLength_InvalidURL(t *testing.T) {
	t.Parallel()

	// Test with an invalid URL
	url := "http://invalid-host-that-does-not-exist.local:99999"
	_, err := HeadURLForContentLength(context.Background(), url, WithTimeout(100*time.Millisecond))
	require.Error(t, err)
}

func TestDownloadURLToFile_InvalidURL(t *testing.T) {
	t.Parallel()

	// Test with an invalid URL
	_, err := DownloadURLToFile(context.Background(), "http://invalid-host-that-does-not-exist.local:99999", WithTimeout(100*time.Millisecond))
	require.Error(t, err)
}

func TestDownloadURL_Timeout(t *testing.T) {
	t.Parallel()

	// Test that timeout is respected
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Sleep longer than the timeout
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	_, err := DownloadURL(context.Background(), ts.URL, WithTimeout(50*time.Millisecond))
	require.Error(t, err)
}

func TestDownloadURLToFile_WithUserAgent(t *testing.T) {
	t.Parallel()

	// Test that User-Agent is properly sent in download to file request
	var receivedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "content")
	}))
	defer ts.Close()

	filePath, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second), WithUserAgent("etcd-infra-file-agent"))
	require.NoError(t, err)
	defer func() { _ = os.Remove(filePath) }()

	assert.Equal(t, "etcd-infra-file-agent", receivedUA)
}

func TestHeadURLForContentLength_ZeroLength(t *testing.T) {
	t.Parallel()

	// Test with Content-Length of 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	contentLength, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(0), contentLength)
}

func TestHeadURLForContentLength_LargeLength(t *testing.T) {
	t.Parallel()

	// Test with a large Content-Length value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "9223372036854775807") // Max int64
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	contentLength, err := HeadURLForContentLength(context.Background(), ts.URL, WithTimeout(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(9223372036854775807), contentLength)
}

func TestOp_DefaultValues(t *testing.T) {
	t.Parallel()

	// Test that Op has proper zero values
	op := &Op{}
	assert.Equal(t, time.Duration(0), op.timeout)
	assert.Empty(t, op.downloadFilePath)
	assert.Empty(t, op.userAgent)
}

func TestOpOptions_EmptySlice(t *testing.T) {
	t.Parallel()

	// Test applying empty options slice
	op := &Op{}
	op.applyOpts(nil)

	assert.Equal(t, time.Duration(0), op.timeout)
	assert.Empty(t, op.downloadFilePath)
	assert.Empty(t, op.userAgent)
}
