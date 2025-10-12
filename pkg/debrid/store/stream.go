package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const (
	MaxNetworkRetries = 5
	// MaxLinkRetries reduced from 10 to 3 to minimize cascading API calls during rate limiting
	// This reduces API calls from 11 to 4 per stream attempt (P1 rate limiting fix)
	MaxLinkRetries = 3

	// jitterPercent defines the percentage of jitter to apply to backoff durations (±20%)
	jitterPercent = 0.2
)

// P1 Fix: Define sentinel errors for typed error checking
var (
	// ErrLinkNotFound indicates the download link has expired or is invalid (404/410)
	ErrLinkNotFound = errors.New("download link not found")

	// ErrBandwidthExceeded indicates bandwidth/traffic limit exceeded
	ErrBandwidthExceeded = errors.New("bandwidth limit exceeded")

	// ErrRateLimit indicates rate limiting from the provider (429)
	ErrRateLimit = errors.New("rate limit exceeded")

	// ErrServerError indicates a server-side error (5xx)
	ErrServerError = errors.New("server error")
)

type StreamError struct {
	Err       error
	Retryable bool
	LinkError bool // true if we should try a new link
}

func (e StreamError) Error() string {
	return e.Err.Error()
}

func (e StreamError) Unwrap() error {
	return e.Err
}

// isConnectionError checks if the error is related to connection issues
func (c *Cache) isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Check for common connection errors
	if strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection refused") {
		return true
	}

	// Check for net.Error types
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (c *Cache) Stream(ctx context.Context, start, end int64, linkFunc func() (types.DownloadLink, error)) (*http.Response, error) {

	var lastErr error

	downloadLink, err := linkFunc()
	if err != nil {
		return nil, fmt.Errorf("failed to get download link: %w", err)
	}

	// Outer loop: Link retries
	for retry := 0; retry < MaxLinkRetries; retry++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resp, err := c.doRequest(ctx, downloadLink.DownloadLink, start, end)
		if err != nil {
			// Network/connection error
			lastErr = err
			c.logger.Trace().
				Int("retries", retry).
				Err(err).
				Msg("Network request failed")

			// Apply exponential backoff with jitter after failure and before next retry
			if retry < MaxLinkRetries-1 {
				// Exponential backoff: 1s, 2s, 4s with ±20% jitter
				backoffDuration := time.Duration(1<<uint(retry)) * time.Second
				jitterRange := float64(backoffDuration) * jitterPercent * 2
				jitterOffset := float64(backoffDuration) * jitterPercent
				jitter := time.Duration(rand.Float64()*jitterRange - jitterOffset)
				sleepDuration := backoffDuration + jitter

				c.logger.Trace().
					Int("retry", retry).
					Dur("backoff", backoffDuration).
					Dur("jitter", jitter).
					Dur("total_sleep", sleepDuration).
					Msg("Applying exponential backoff after failure")

				select {
				case <-time.After(sleepDuration):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			continue
		}

		// Got response - check status
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			// Success! Clear validation retry counter
			c.clearValidationRetry(downloadLink.DownloadLink)
			return resp, nil
		}

		// Bad status code - handle error
		streamErr := c.handleHTTPError(resp, downloadLink)
		resp.Body.Close()

		if !streamErr.Retryable {
			return nil, streamErr // Fatal error
		}

		if streamErr.LinkError {
			// Check exponential backoff before fetching new link
			if !c.shouldRetryInvalidLink(downloadLink.DownloadLink) {
				c.logger.Debug().
					Int("retries", retry).
					Str("downloadLink", downloadLink.DownloadLink).
					Msg("Exponential backoff in effect, skipping new link fetch")
				return nil, fmt.Errorf("link retry backoff in effect: %w", streamErr)
			}

			c.logger.Trace().
				Int("retries", retry).
				Msg("Link error, getting fresh link")
			lastErr = streamErr

			// Record the retry attempt for exponential backoff
			c.recordInvalidLinkRetry(downloadLink.DownloadLink)

			// Try new link - don't count validation retries against link retry budget
			// Reset retry counter since we're starting fresh with a new link
			retry = -1 // Will be incremented to 0 at top of loop

			downloadLink, err = linkFunc()
			if err != nil {
				return nil, fmt.Errorf("failed to get download link: %w", err)
			}
			continue
		}

		// Retryable HTTP error (429, 503, etc.) - apply backoff before retry
		lastErr = streamErr
		c.logger.Trace().
			Err(lastErr).
			Str("downloadLink", downloadLink.DownloadLink).
			Str("link", downloadLink.Link).
			Int("retries", retry).
			Int("statusCode", resp.StatusCode).
			Msg("HTTP error, will retry after backoff")

		// Apply exponential backoff before next retry
		if retry < MaxLinkRetries-1 {
			// Exponential backoff: 1s, 2s, 4s, capped at 10s with ±20% jitter
			backoffDuration := time.Duration(1<<uint(retry)) * time.Second
			if backoffDuration > 10*time.Second {
				backoffDuration = 10 * time.Second
			}
			jitterRange := float64(backoffDuration) * jitterPercent * 2
			jitterOffset := float64(backoffDuration) * jitterPercent
			jitter := time.Duration(rand.Float64()*jitterRange - jitterOffset)
			sleepDuration := backoffDuration + jitter

			c.logger.Trace().
				Int("retry", retry).
				Dur("backoff", backoffDuration).
				Dur("jitter", jitter).
				Dur("total_sleep", sleepDuration).
				Msg("Applying exponential backoff for HTTP error")

			select {
			case <-time.After(sleepDuration):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, fmt.Errorf("stream failed after %d link retries: %w", MaxLinkRetries, lastErr)
}

func (c *Cache) StreamReader(ctx context.Context, start, end int64, linkFunc func() (types.DownloadLink, error)) (io.ReadCloser, error) {
	resp, err := c.Stream(ctx, start, end, linkFunc)
	if err != nil {
		return nil, err
	}

	// Validate we got the expected content
	if resp.ContentLength == 0 {
		resp.Body.Close()
		return nil, fmt.Errorf("received empty response")
	}

	return resp.Body, nil
}

func (c *Cache) doRequest(ctx context.Context, url string, start, end int64) (*http.Response, error) {
	var lastErr error
	// Retry loop specifically for connection-level failures (EOF, reset, etc.)
	for connRetry := 0; connRetry < 3; connRetry++ {
		// Check context cancellation before each retry
		select {
		case <-ctx.Done():
			return nil, StreamError{Err: ctx.Err(), Retryable: false}
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, StreamError{Err: err, Retryable: false}
		}

		// Set range header
		if start > 0 || end > 0 {
			rangeHeader := fmt.Sprintf("bytes=%d-", start)
			if end > 0 {
				rangeHeader = fmt.Sprintf("bytes=%d-%d", start, end)
			}
			req.Header.Set("Range", rangeHeader)
		}

		// Set optimized headers for streaming
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Accept-Encoding", "identity") // Disable compression for streaming
		req.Header.Set("Cache-Control", "no-cache")

		resp, err := c.streamClient.Do(req)
		if err != nil {
			lastErr = err

			// Check if it's a connection error that we should retry
			if c.isConnectionError(err) && connRetry < 2 {
				// Exponential backoff: 100ms, 200ms, 400ms with ±20% jitter
				backoffDuration := time.Duration(100<<uint(connRetry)) * time.Millisecond
				jitterRange := float64(backoffDuration) * jitterPercent * 2
				jitterOffset := float64(backoffDuration) * jitterPercent
				jitter := time.Duration(rand.Float64()*jitterRange - jitterOffset)
				sleepDuration := backoffDuration + jitter

				c.logger.Trace().
					Int("retry", connRetry).
					Dur("backoff", backoffDuration).
					Dur("jitter", jitter).
					Dur("total_sleep", sleepDuration).
					Msg("Connection error, applying exponential backoff")

				select {
				case <-time.After(sleepDuration):
				case <-ctx.Done():
					return nil, StreamError{Err: ctx.Err(), Retryable: false}
				}
				continue
			}

			return nil, StreamError{Err: err, Retryable: true}
		}
		return resp, nil
	}

	return nil, StreamError{Err: fmt.Errorf("connection retry exhausted: %w", lastErr), Retryable: true}
}

func (c *Cache) handleHTTPError(resp *http.Response, downloadLink types.DownloadLink) StreamError {
	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.ToLower(string(body))

	switch resp.StatusCode {
	case http.StatusNotFound:
		// Try to validate the link before marking it invalid
		if c.shouldValidateLink(downloadLink.DownloadLink) {
			// Retry with the same link (validation attempt)
			c.logger.Debug().
				Str("downloadLink", downloadLink.DownloadLink).
				Msg("Link returned 404, retrying for validation")
			return StreamError{
				Err:       errors.New("download link not found, retrying for validation"),
				Retryable: true,
				LinkError: false, // Don't fetch new link yet, retry same link
			}
		}

		// Max validation retries reached, mark as invalid
		c.MarkLinkAsInvalid(downloadLink, "link_not_found")
		return StreamError{
			Err:       ErrLinkNotFound, // P1 Fix: Use sentinel error
			Retryable: true,
			LinkError: true, // Fetch new link
		}

	case http.StatusBadRequest:
		// Handle TorBox's "Invalid Presigned Token" error
		if strings.Contains(bodyStr, "invalid presigned token") ||
		   strings.Contains(bodyStr, "presigned") {
			// Try to validate the link before marking it invalid
			if c.shouldValidateLink(downloadLink.DownloadLink) {
				// Retry with the same link (validation attempt)
				c.logger.Debug().
					Str("downloadLink", downloadLink.DownloadLink).
					Msg("Link returned 400 (invalid presigned token), retrying for validation")
				return StreamError{
					Err:       errors.New("invalid presigned token, retrying for validation"),
					Retryable: true,
					LinkError: false, // Don't fetch new link yet, retry same link
				}
			}

			// Max validation retries reached, mark as invalid
			c.MarkLinkAsInvalid(downloadLink, "invalid_presigned_token")
			return StreamError{
				Err:       fmt.Errorf("invalid presigned token: %w", ErrLinkNotFound),
				Retryable: true,
				LinkError: true, // Fetch new link
			}
		}
		// Fall through to default for other 400 errors
		fallthrough

	case http.StatusServiceUnavailable:
		if strings.Contains(bodyStr, "bandwidth") || strings.Contains(bodyStr, "traffic") {
			// Bandwidth errors should immediately mark as invalid (no validation retry)
			c.MarkLinkAsInvalid(downloadLink, "bandwidth_exceeded")
			return StreamError{
				Err:       ErrBandwidthExceeded, // P1 Fix: Use sentinel error
				Retryable: true,
				LinkError: true,
			}
		}
		fallthrough

	case http.StatusTooManyRequests:
		return StreamError{
			Err:       fmt.Errorf("%w: HTTP %d", ErrRateLimit, resp.StatusCode), // P1 Fix: Wrap sentinel error
			Retryable: true,
			LinkError: false,
		}

	default:
		retryable := resp.StatusCode >= 500
		if retryable {
			return StreamError{
				Err:       fmt.Errorf("%w: HTTP %d: %s", ErrServerError, resp.StatusCode, string(body)), // P1 Fix: Wrap sentinel error
				Retryable: true,
				LinkError: false,
			}
		}
		return StreamError{
			Err:       fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)),
			Retryable: false,
			LinkError: false,
		}
	}
}
