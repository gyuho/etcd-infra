package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"go.uber.org/zap"

	"git.tbd/etcd-infra/pkg/log"
)

var (
	// ErrFailedGET is returned when a GET request fails.
	ErrFailedGET = errors.New("failed to GET")
	// ErrFailedDownloadFile is returned when a download fails.
	ErrFailedDownloadFile = errors.New("failed to download file")
	// ErrFailedHEAD is returned when a HEAD request fails.
	ErrFailedHEAD = errors.New("failed HEAD")
	// ErrMissingContentLength is returned when Content-Length header is missing.
	ErrMissingContentLength = errors.New("Content-Length header is missing")
)

// DownloadURL downloads the content from the specified URL and returns it as a byte slice.
func DownloadURL(ctx context.Context, url string, opts ...OpOption) ([]byte, error) {
	options := &Op{}
	options.applyOpts(opts)

	client := http.Client{
		Timeout: options.timeout,
	}

	req, err := newRequest(ctx, http.MethodGet, url, options)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download URL: %w", err)
	}
	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			log.L().Warn("failed to close response body", zap.Error(closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", ErrFailedGET, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	return data, nil
}

// DownloadURLToFile downloads the content from the specified URL and saves it to a file.
func DownloadURLToFile(ctx context.Context, url string, opts ...OpOption) (string, error) {
	options := &Op{}
	options.applyOpts(opts)

	client := http.Client{
		Timeout: options.timeout,
	}

	req, err := newRequest(ctx, http.MethodGet, url, options)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download URL to file: %w", err)
	}
	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			log.L().Warn("failed to close response body", zap.Error(closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s", ErrFailedDownloadFile, resp.Status)
	}

	var f *os.File
	if options.downloadFilePath == "" {
		f, err = os.CreateTemp(os.TempDir(), "download")
	} else {
		f, err = os.Create(options.downloadFilePath)
	}
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer func() {
		closeErr := f.Close()
		if closeErr != nil {
			log.L().Warn("failed to close file", zap.Error(closeErr))
		}
	}()

	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return "", fmt.Errorf("copy to file: %w", err)
	}

	return f.Name(), nil
}

// HeadURLForContentLength sends a HEAD request to the specified URL and returns the content length.
func HeadURLForContentLength(ctx context.Context, url string, opts ...OpOption) (int64, error) {
	options := &Op{}
	options.applyOpts(opts)

	client := http.Client{
		Timeout: options.timeout,
	}

	req, err := newRequest(ctx, http.MethodHead, url, options)
	if err != nil {
		return 0, err
	}
	headResp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("head URL: %w", err)
	}
	defer func() {
		closeErr := headResp.Body.Close()
		if closeErr != nil {
			log.L().Warn("failed to close response body", zap.Error(closeErr))
		}
	}()

	if headResp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: %s", ErrFailedHEAD, headResp.Status)
	}

	contentLength := headResp.Header.Get("Content-Length")
	if contentLength == "" {
		return 0, ErrMissingContentLength
	}

	size, err := strconv.ParseInt(contentLength, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse content length: %w", err)
	}

	return size, nil
}

func newRequest(ctx context.Context, method, url string, options *Op) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if options != nil && options.userAgent != "" {
		req.Header.Set("User-Agent", options.userAgent)
	}

	return req, nil
}
