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

// musicbrainzClient bridges a Spotify track's ISRC to any local file
// tagged with the corresponding MusicBrainz Recording ID, via
// MusicBrainz's ISRC lookup (the reverse of looking up a recording's
// ISRCs — this direction is what lets resolution stay scoped to the
// tracks actually being matched, rather than every MusicBrainz-tagged
// file in the whole library; see RecordingsForISRC). MusicBrainz's
// documented rate limit for API clients is 1 request/second; the limiter
// enforces that regardless of caller concurrency.
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

type mbISRCResponse struct {
	Recordings []struct {
		ID string `json:"id"`
	} `json:"recordings"`
}

// RecordingsForISRC returns the MusicBrainz Recording IDs associated with
// an ISRC. An ISRC MusicBrainz has never seen returns (nil, nil) — a
// normal, non-error outcome, not every ISRC is in their database.
func (c *musicbrainzClient) RecordingsForISRC(ctx context.Context, isrc string) ([]string, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://musicbrainz.org/ws/2/isrc/%s?fmt=json", url.PathEscape(isrc))
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
		return nil, nil // ISRC not in MusicBrainz — not a hard error
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz api returned %d for isrc %s", resp.StatusCode, isrc)
	}

	var out mbISRCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding musicbrainz response for %s: %w", isrc, err)
	}

	ids := make([]string, len(out.Recordings))
	for i, r := range out.Recordings {
		ids[i] = r.ID
	}
	return ids, nil
}
