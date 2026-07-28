//nolint:testpackage,paralleltest // Mockey patches global functions, requires sequential execution.
package metadata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bytedance/mockey" //nolint:depguard // mock library for runtime function patching
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test error variables.
var (
	errMockHTTPFailed = errors.New("http request failed")
	errMockReadFailed = errors.New("read failed")
)

const testInstanceID = "i-1234567890abcdef0"

func TestFetchToken_Success(t *testing.T) {
	mockey.PatchConvey("TestFetchToken_Success", t, func() {
		expectedToken := "test-token-12345"

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, "21600", req.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds"))
			assert.Equal(t, http.MethodPut, req.Method)
			assert.Equal(t, imdsV2SessionTokenURI, req.URL.String())

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(expectedToken)),
			}, nil
		}).Build()

		ctx := context.Background()
		token, err := fetchToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, expectedToken, token)
	})
}

func TestFetchToken_HTTPError(t *testing.T) {
	mockey.PatchConvey("TestFetchToken_HTTPError", t, func() {
		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			return nil, errMockHTTPFailed
		}).Build()

		ctx := context.Background()
		token, err := fetchToken(ctx)
		require.Error(t, err)
		assert.Empty(t, token)
		assert.Contains(t, err.Error(), "http request failed")
	})
}

func TestFetchToken_NonOKStatus(t *testing.T) {
	mockey.PatchConvey("TestFetchToken_NonOKStatus", t, func() {
		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("forbidden")),
			}, nil
		}).Build()

		ctx := context.Background()
		token, err := fetchToken(ctx)
		require.Error(t, err)
		assert.Empty(t, token)
		assert.Contains(t, err.Error(), "HTTP 403")
	})
}

func TestFetchToken_ReadError(t *testing.T) {
	mockey.PatchConvey("TestFetchToken_ReadError", t, func() {
		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(errReader{}),
			}, nil
		}).Build()

		ctx := context.Background()
		token, err := fetchToken(ctx)
		require.Error(t, err)
		assert.Empty(t, token)
	})
}

func TestFetchPath_Success(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_Success", t, func() {
		expectedToken := "test-token"
		expectedMetadata := testInstanceID
		callCount := 0

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				assert.Equal(t, http.MethodPut, req.Method)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(expectedToken)),
				}, nil
			}
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, expectedToken, req.Header.Get("X-Aws-Ec2-Metadata-Token"))
			assert.Contains(t, req.URL.String(), "169.254.169.254/latest/meta-data/")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(expectedMetadata)),
			}, nil
		}).Build()

		ctx := context.Background()
		result, err := fetchPath(ctx, "instance-id")
		require.NoError(t, err)
		assert.Equal(t, expectedMetadata, result)
		assert.Equal(t, 2, callCount, "should make 2 HTTP calls: token + metadata")
	})
}

func TestFetchPath_NonOKMetadataStatus(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_NonOKMetadataStatus", t, func() {
		callCount := 0

		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("Not Found")),
			}, nil
		}).Build()

		ctx := context.Background()
		result, err := fetchPath(ctx, "spot/instance-action")
		require.Error(t, err)
		assert.Empty(t, result)
		assert.Contains(t, err.Error(), "HTTP 404")
	})
}

func TestFetchPath_PathNormalization_FullPath(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_PathNormalization_FullPath", t, func() {
		var capturedURL string

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			capturedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data")),
			}, nil
		}).Build()

		ctx := context.Background()
		_, err := fetchPath(ctx, "/latest/meta-data/instance-id")
		require.NoError(t, err)
		assert.Equal(t, "http://169.254.169.254/latest/meta-data/instance-id", capturedURL)
	})
}

func TestFetchPath_PathNormalization_SimplePath(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_PathNormalization_SimplePath", t, func() {
		var capturedURL string

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			capturedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data")),
			}, nil
		}).Build()

		ctx := context.Background()
		_, err := fetchPath(ctx, "instance-id")
		require.NoError(t, err)
		assert.Equal(t, "http://169.254.169.254/latest/meta-data/instance-id", capturedURL)
	})
}

func TestFetchPath_PathNormalization_LeadingSlash(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_PathNormalization_LeadingSlash", t, func() {
		var capturedURL string

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			capturedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data")),
			}, nil
		}).Build()

		ctx := context.Background()
		_, err := fetchPath(ctx, "/instance-id")
		require.NoError(t, err)
		assert.Equal(t, "http://169.254.169.254/latest/meta-data/instance-id", capturedURL)
	})
}

func TestFetchPath_PathNormalization_Nested(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_PathNormalization_Nested", t, func() {
		var capturedURL string

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			capturedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data")),
			}, nil
		}).Build()

		ctx := context.Background()
		_, err := fetchPath(ctx, "placement/availability-zone")
		require.NoError(t, err)
		assert.Equal(t, "http://169.254.169.254/latest/meta-data/placement/availability-zone", capturedURL)
	})
}

func TestFetchPath_TokenError(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_TokenError", t, func() {
		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPut {
				return nil, errMockHTTPFailed
			}
			return nil, errors.New("should not reach metadata request")
		}).Build()

		ctx := context.Background()
		result, err := fetchPath(ctx, "instance-id")
		require.Error(t, err)
		assert.Empty(t, result)
	})
}

func TestFetchPath_MetadataError(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_MetadataError", t, func() {
		callCount := 0

		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("token")),
				}, nil
			}
			return nil, errMockHTTPFailed
		}).Build()

		ctx := context.Background()
		result, err := fetchPath(ctx, "instance-id")
		require.Error(t, err)
		assert.Empty(t, result)
	})
}

func TestFetchInstanceID_Success(t *testing.T) {
	mockey.PatchConvey("TestFetchInstanceID_Success", t, func() {
		expectedID := testInstanceID
		setupMockMetadataResponse(expectedID)

		ctx := context.Background()
		instanceID, err := FetchInstanceID(ctx)
		require.NoError(t, err)
		assert.Equal(t, expectedID, instanceID)
	})
}

func TestFetchInstanceID_Empty(t *testing.T) {
	mockey.PatchConvey("TestFetchInstanceID_Empty", t, func() {
		setupMockMetadataResponse("")

		ctx := context.Background()
		instanceID, err := FetchInstanceID(ctx)
		require.NoError(t, err)
		assert.Empty(t, instanceID)
	})
}

func TestFetchAvailabilityZone_Success(t *testing.T) {
	mockey.PatchConvey("TestFetchAvailabilityZone_Success", t, func() {
		expectedAZ := "us-east-1a"
		setupMockMetadataResponse(expectedAZ)

		ctx := context.Background()
		az, err := FetchAvailabilityZone(ctx)
		require.NoError(t, err)
		assert.Equal(t, expectedAZ, az)
	})
}

func TestFetchRegion_USEast1(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_USEast1", t, func() {
		setupMockMetadataResponse("us-east-1a")

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.NoError(t, err)
		assert.Equal(t, "us-east-1", region)
	})
}

func TestFetchRegion_USWest2(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_USWest2", t, func() {
		setupMockMetadataResponse("us-west-2b")

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.NoError(t, err)
		assert.Equal(t, "us-west-2", region)
	})
}

func TestFetchRegion_EUCentral1(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_EUCentral1", t, func() {
		setupMockMetadataResponse("eu-central-1c")

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.NoError(t, err)
		assert.Equal(t, "eu-central-1", region)
	})
}

func TestFetchRegion_Error(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_Error", t, func() {
		mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
			return nil, errMockHTTPFailed
		}).Build()

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.Error(t, err)
		assert.Empty(t, region)
	})
}

func TestFetchRegion_EmptyAZ(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_EmptyAZ", t, func() {
		setupMockMetadataResponse("")

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.Error(t, err)
		assert.Empty(t, region)
		assert.Contains(t, err.Error(), "empty availability zone")
	})
}

func TestFetchToken_ContextPropagated(t *testing.T) {
	mockey.PatchConvey("TestFetchToken_ContextPropagated", t, func() {
		type ctxKey struct{}
		ctx := context.WithValue(context.Background(), ctxKey{}, "present")

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			// Verify the context was set directly on the request
			// (not via a later WithContext call).
			val, ok := req.Context().Value(ctxKey{}).(string)
			assert.True(t, ok, "context key must be present on request")
			assert.Equal(t, "present", val)

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("tok")),
			}, nil
		}).Build()

		token, err := fetchToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, "tok", token)
	})
}

func TestFetchPath_ContextPropagated(t *testing.T) {
	mockey.PatchConvey("TestFetchPath_ContextPropagated", t, func() {
		type ctxKey struct{}
		ctx := context.WithValue(context.Background(), ctxKey{}, "present")

		mockey.Mock(doRequest).To(func(req *http.Request) (*http.Response, error) {
			val, ok := req.Context().Value(ctxKey{}).(string)
			assert.True(t, ok, "context key must be present on request")
			assert.Equal(t, "present", val)

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("data")),
			}, nil
		}).Build()

		result, err := fetchPath(ctx, "instance-id")
		require.NoError(t, err)
		assert.Equal(t, "data", result)
	})
}

func TestFetchRegion_SingleCharAZ(t *testing.T) {
	mockey.PatchConvey("TestFetchRegion_SingleCharAZ", t, func() {
		setupMockMetadataResponse("a")

		ctx := context.Background()
		region, err := FetchRegion(ctx)
		require.NoError(t, err)
		assert.Empty(t, region, "single-char AZ should produce empty region")
	})
}

// setupMockMetadataResponse is a helper to set up standard mock responses
// for a two-call sequence (token + metadata).
func setupMockMetadataResponse(response string) {
	callCount := 0
	mockey.Mock(doRequest).To(func(_ *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("test-token")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	}).Build()
}

// errReader is an io.Reader that always returns an error.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errMockReadFailed
}
