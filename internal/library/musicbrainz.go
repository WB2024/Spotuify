package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	endpoint := fmt.Sprintf("https://musicbrainz.org/ws/2/isrc/%s?fmt=json", url.PathEscape(isrc))

	var out mbISRCResponse
	found, err := c.getJSON(ctx, endpoint, &out)
	if err != nil {
		return nil, fmt.Errorf("isrc %s: %w", isrc, err)
	}
	if !found {
		return nil, nil // ISRC not in MusicBrainz — not a hard error
	}

	ids := make([]string, len(out.Recordings))
	for i, r := range out.Recordings {
		ids[i] = r.ID
	}
	return ids, nil
}

type mbSearchResponse struct {
	Count      int `json:"count"`
	Recordings []struct {
		ID    string   `json:"id"`
		ISRCs []string `json:"isrcs"`
	} `json:"recordings"`
}

// RecordingsForISRCs resolves many ISRCs in as few requests as possible via
// MusicBrainz's recording search service (GET /ws/2/recording?query=...),
// instead of the one-ISRC-per-request /ws/2/isrc/{isrc} lookup
// RecordingsForISRC uses. The search service accepts full Lucene syntax, so
// many ISRCs can be OR'd into a single query (e.g. "isrc:A OR isrc:B OR
// ..."), and inc=isrcs returns each matching recording's complete ISRC list
// - intersected against the input batch here to rebuild the same isrc ->
// recording-IDs mapping N separate lookups would have produced, in the one
// request this still costs against the 1 request/second limit (see
// newMusicBrainzClient). Verified for real against the public API before
// relying on it: a 39-ISRC batch (real ISRCs from this user's own exported
// playlists) correctly resolved 39/40 in one request - the other genuinely
// isn't in MusicBrainz - returning 52 recordings, comfortably under the
// search service's 100-result cap even with a few ISRCs mapping to more
// than one recording. Callers should keep batches to at most
// MBISRCBatchSize so that even a batch with heavier-than-usual fan-out
// still can't realistically exceed that cap and silently lose a match.
// An ISRC MusicBrainz has never seen just has no entry in the returned map
// - not an error, same as RecordingsForISRC's (nil, nil) for that case.
func (c *musicbrainzClient) RecordingsForISRCs(ctx context.Context, isrcs []string) (map[string][]string, error) {
	if len(isrcs) == 0 {
		return nil, nil
	}

	wanted := make(map[string]bool, len(isrcs))
	terms := make([]string, 0, len(isrcs))
	for _, isrc := range isrcs {
		if wanted[isrc] {
			continue
		}
		wanted[isrc] = true
		terms = append(terms, "isrc:"+isrc)
	}
	query := strings.Join(terms, " OR ")

	out := make(map[string][]string)
	offset := 0
	for {
		endpoint := fmt.Sprintf("https://musicbrainz.org/ws/2/recording/?query=%s&fmt=json&inc=isrcs&limit=100&offset=%d", url.QueryEscape(query), offset)

		var resp mbSearchResponse
		found, err := c.getJSON(ctx, endpoint, &resp)
		if err != nil {
			return nil, fmt.Errorf("batch isrc search (%d isrcs): %w", len(terms), err)
		}
		if !found || len(resp.Recordings) == 0 {
			break
		}

		for _, rec := range resp.Recordings {
			for _, isrc := range rec.ISRCs {
				if wanted[isrc] {
					out[isrc] = append(out[isrc], rec.ID)
				}
			}
		}

		offset += len(resp.Recordings)
		if offset >= resp.Count {
			break
		}
	}

	return out, nil
}

// ReleaseGroup is a MusicBrainz release group — the "album" as a work,
// independent of any particular pressing/edition — which is also exactly
// what Lidarr keys its albums by (its foreignAlbumId).
type ReleaseGroup struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	PrimaryType      string   `json:"primary_type"`    // Album, Single, EP, ...
	SecondaryTypes   []string `json:"secondary_types"` // Compilation, Soundtrack, Live, ...
	FirstReleaseDate string   `json:"first_release_date"`
}

type mbRecordingResponse struct {
	Releases []struct {
		ReleaseGroup struct {
			ID               string   `json:"id"`
			Title            string   `json:"title"`
			PrimaryType      string   `json:"primary-type"`
			SecondaryTypes   []string `json:"secondary-types"`
			FirstReleaseDate string   `json:"first-release-date"`
		} `json:"release-group"`
	} `json:"releases"`
}

// ReleaseGroupsForRecording returns every release group a recording
// appears on (the album it's from, plus any singles, compilations, and
// soundtracks that reused it), in MusicBrainz's order, deduplicated.
func (c *musicbrainzClient) ReleaseGroupsForRecording(ctx context.Context, recordingID string) ([]ReleaseGroup, error) {
	endpoint := fmt.Sprintf("https://musicbrainz.org/ws/2/recording/%s?fmt=json&inc=releases+release-groups", url.PathEscape(recordingID))

	var out mbRecordingResponse
	found, err := c.getJSON(ctx, endpoint, &out)
	if err != nil {
		return nil, fmt.Errorf("recording %s: %w", recordingID, err)
	}
	if !found {
		return nil, nil
	}

	seen := make(map[string]bool, len(out.Releases))
	var groups []ReleaseGroup
	for _, rel := range out.Releases {
		rg := rel.ReleaseGroup
		if rg.ID == "" || seen[rg.ID] {
			continue
		}
		seen[rg.ID] = true
		groups = append(groups, ReleaseGroup{
			ID:               rg.ID,
			Title:            rg.Title,
			PrimaryType:      rg.PrimaryType,
			SecondaryTypes:   rg.SecondaryTypes,
			FirstReleaseDate: rg.FirstReleaseDate,
		})
	}
	return groups, nil
}

// getJSON performs one rate-limited MusicBrainz request. found is false on
// a 404 (the entity isn't in MusicBrainz — a normal outcome, not an error).
func (c *musicbrainzClient) getJSON(ctx context.Context, endpoint string, out any) (found bool, err error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	// MusicBrainz's API etiquette requires a descriptive User-Agent with a
	// way to reach the app's maintainer; generic/absent UAs get throttled
	// harder. https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting
	req.Header.Set("User-Agent", "Spotuify/0.1 (+https://github.com/WB2024/Spotuify)")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("musicbrainz api returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("decoding musicbrainz response: %w", err)
	}
	return true, nil
}
