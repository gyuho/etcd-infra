//nolint:testpackage,paralleltest
package httputil

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	httptest "git.tbd/etcd-infra/pkg/testutil/httptest"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadURL_ReadAllError(t *testing.T) {
	mockey.PatchConvey("io.ReadAll error", t, func() {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("content"))
		}))
		defer ts.Close()

		mockey.Mock(io.ReadAll).Return(nil, io.ErrUnexpectedEOF).Build()

		_, err := DownloadURL(context.Background(), ts.URL, WithTimeout(5*time.Second))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read response body")
	})
}

func TestDownloadURLToFile_CopyError(t *testing.T) {
	mockey.PatchConvey("io.Copy error", t, func() {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("content"))
		}))
		defer ts.Close()

		mockey.Mock(io.Copy).Return(int64(0), io.ErrClosedPipe).Build()

		_, err := DownloadURLToFile(context.Background(), ts.URL, WithTimeout(5*time.Second))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "copy to file")
	})
}
