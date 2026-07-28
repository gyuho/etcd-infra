package metadata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// imdsV2SessionTokenURI is the IMDS v2 token endpoint.
// ref. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-service.html
const imdsV2SessionTokenURI = "http://169.254.169.254/latest/api/token"

// doRequest executes an HTTP request. Defined as a standalone function so
// mockey can patch it in tests.
func doRequest(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req) //nolint:wrapcheck
}

// fetchToken fetches the IMDS v2 session token.
func fetchToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, imdsV2SessionTokenURI, nil)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	req.Header.Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")

	resp, err := doRequest(req)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IMDS token request returned HTTP %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	return string(b), nil
}

// fetchPath fetches an IMDS v2 metadata path.
// ref. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html
func fetchPath(ctx context.Context, path string) (string, error) {
	path = strings.TrimPrefix(path, "/latest/meta-data/")
	path = strings.TrimPrefix(path, "/")
	uri := "http://169.254.169.254/latest/meta-data/" + path

	logutil.S().Infow("fetching meta-data", "uri", uri)

	token, err := fetchToken(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	req.Header.Set("X-Aws-Ec2-Metadata-Token", token)

	resp, err := doRequest(req)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IMDS %s returned HTTP %d", uri, resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err //nolint:wrapcheck
	}
	return string(b), nil
}

// FetchInstanceID fetches the instance ID from IMDS.
// ref. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-categories.html
func FetchInstanceID(ctx context.Context) (string, error) {
	return fetchPath(ctx, "instance-id")
}

// FetchAvailabilityZone fetches the availability zone from IMDS.
// ref. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-categories.html
func FetchAvailabilityZone(ctx context.Context) (string, error) {
	return fetchPath(ctx, "placement/availability-zone")
}

// FetchRegion fetches the region from IMDS by stripping the trailing AZ
// suffix letter from the availability zone string (e.g. "us-east-1a" -> "us-east-1").
// ref. https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-categories.html
func FetchRegion(ctx context.Context) (string, error) {
	az, err := FetchAvailabilityZone(ctx)
	if err != nil {
		return "", err
	}
	if az == "" {
		return "", errors.New("IMDS returned empty availability zone")
	}
	return az[:len(az)-1], nil
}
