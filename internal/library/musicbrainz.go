package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

// musicbrainzClient resolves a MusicBrainz Recording ID to its ISRC(s),
// bridging a local file's embedded MusicBrainz tag to Spotify's ISRC
// metadata. MusicBrainz's documented rate limit for API clients is 1
// request/second; limiter enforces that regardless of caller concurrency.
type musicbrainzClient struct {
	http    *http.Client
	limiter *rate.Limiter
}

func newMusicBrainzClient() *musicbrainzClient {
	return &musicbrainzClient{
		http:    &http.Client{Timeout: 15 * time.Second},
		limiter: rate.NewLimiter(rate.Every(1100*time.Millisecond), 1),
	}
}

type mbRecordingResponse struct {
	ISRCs []string `json:"isrcs"`
}

// ResolveISRCs fetches every ISRC MusicBrainz has on file for a recording.
// A recording with no ISRC data returns (nil, nil) — a normal, non-error
// outcome, not every recording has one recorded in MusicBrainz.
func (c *musicbrainzClient) ResolveISRCs(ctx context.Context, mbid string) ([]string, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://musicbrainz.org/ws/2/recording/%s?inc=isrcs&fmt=json", url.PathEscape(mbid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// MusicBrainz's API etiquette requires a descriptive User-Agent with a
	// way to reach the app's maintainer; generic/absent UAs get throttled
	// harder. https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
	req.Header.Set("User-Agent", "Spotuify/0.1 (+https://github.com/WB2024/Spotuify)")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // recording doesn't exist (deleted/merged) — not a hard error
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz api returned %d for recording %s", resp.StatusCode, mbid)
	}

	var out mbRecordingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding musicbrainz response for %s: %w", mbid, err)
	}
	return out.ISRCs, nil
}
