// Package spotifyapi is a small, purpose-built client for the parts of the
// Spotify Web API that Spotuify needs: listing a user's playlists and
// reading every field of a playlist and its tracks.
//
// It's deliberately not a general-purpose SDK. It respects Spotify's rate
// limiting contract (https://developer.spotify.com/documentation/web-api/concepts/rate-limits):
// Spotify doesn't publish a fixed requests/second number, just a rolling
// 30-second window per app; the documented way to stay within it is to back
// off using the Retry-After header on 429s, which is what doRequest does.
package spotifyapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const baseURL = "https://api.spotify.com/v1"

// Client wraps an authenticated *http.Client with rate-limit backoff and
// JSON decoding helpers.
type Client struct {
	http *http.Client
	// limiter self-throttles outgoing requests to a conservative steady
	// rate so we rarely hit a 429 in the first place. Spotify's actual
	// limit is an undocumented rolling window per app; this is a polite
	// default, not a guarantee.
	limiter *rate.Limiter
}

// New wraps an OAuth-authenticated http.Client (see internal/auth) for use
// against the Spotify Web API.
func New(httpClient *http.Client) *Client {
	return &Client{
		http:    httpClient,
		limiter: rate.NewLimiter(rate.Every(150*time.Millisecond), 5), // ~6-7 req/s steady, small burst
	}
}

// maxRetryAfterWait bounds how long a single 429 is worth silently sleeping
// through before retrying. Spotify's Retry-After is usually a few seconds
// mid-batch, which this transparently waits out - but once an app is
// properly rate-limited it can come back with a wait measured in *hours*
// (observed firsthand: 16944s, ~4h42m, after a large batch match run).
// Sleeping through that would leave the UI stuck on "Loading..." for hours
// with no indication anything's wrong, so a wait longer than this fails
// immediately instead, with a clear message saying when to try again.
const maxRetryAfterWait = 20 * time.Second

// get issues a GET request against the Spotify Web API and decodes the JSON
// response body into out. It retries on 429 (honoring Retry-After, up to
// maxRetryAfterWait) and on transient 5xx errors, with a small bounded
// number of attempts.
func (c *Client) get(ctx context.Context, path string, out any) error {
	const maxAttempts = 6

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := retryAfter(resp.Header, 2*time.Second)
			resp.Body.Close()
			if wait > maxRetryAfterWait {
				return fmt.Errorf("rate limited by Spotify until %s (in %s) - wait and try again",
					time.Now().Add(wait).Format("15:04"), wait.Round(time.Second))
			}
			lastErr = fmt.Errorf("rate limited by Spotify (attempt %d/%d), waited %s", attempt, maxAttempts, wait)
			if err := sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			wait := time.Duration(attempt) * time.Second
			lastErr = fmt.Errorf("spotify server error %d (attempt %d/%d)", resp.StatusCode, attempt, maxAttempts)
			if err := sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return &APIError{StatusCode: resp.StatusCode, Path: path, Body: string(body)}
		}

		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response from %s: %w", path, err)
		}
		return nil
	}

	return fmt.Errorf("giving up on %s after %d attempts: %w", path, maxAttempts, lastErr)
}

// APIError is returned for any non-2xx, non-retried Spotify response.
type APIError struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("spotify api error %d on %s: %s", e.StatusCode, e.Path, e.Body)
}

// NotFound reports whether an error represents Spotify's 404 (helpful for
// callers that want to skip missing/deleted playlists rather than aborting
// an export).
func NotFound(err error) bool {
	var apiErr *APIError
	if ok := asAPIError(err, &apiErr); ok {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// Forbidden reports whether an error represents Spotify's 403, typically
// meaning the app doesn't have (or the user hasn't granted, or Spotify has
// restricted for new apps) access to a given field or endpoint.
func Forbidden(err error) bool {
	var apiErr *APIError
	if ok := asAPIError(err, &apiErr); ok {
		return apiErr.StatusCode == http.StatusForbidden
	}
	return false
}

func asAPIError(err error, target **APIError) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	*target = apiErr
	return true
}

func retryAfter(h http.Header, fallback time.Duration) time.Duration {
	raw := h.Get("Retry-After")
	if raw == "" {
		return fallback
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return fallback
	}
	return time.Duration(secs) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
