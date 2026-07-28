package installer

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
)

const latestReleaseURL = "https://github.com/etcd-io/etcd/releases/latest"

// LatestVersion resolves the latest stable etcd release tag.
func LatestVersion(ctx context.Context) (string, error) {
	return resolveLatestVersion(ctx, http.DefaultClient, latestReleaseURL)
}

func resolveLatestVersion(ctx context.Context, client *http.Client, releaseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, releaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("create latest release request: %w", err)
	}
	req.Header.Set("User-Agent", "etcd-infra")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest etcd release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve latest etcd release: unexpected status %s", resp.Status)
	}

	version := path.Base(resp.Request.URL.Path)
	if !strings.HasPrefix(version, "v") || strings.Count(version, ".") < 2 {
		return "", fmt.Errorf("resolve latest etcd release: invalid tag %q", version)
	}
	return version, nil
}
