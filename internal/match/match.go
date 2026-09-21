// Package match maps Spotify playlist tracks to local library files.
//
// Matching is tiered:
//
//  1. ISRC (tagged) — Spotify reports an ISRC (external_ids.isrc) on
//     nearly every track, and the local library index has it wherever
//     Navidrome itself extracted one from a file's own tags. If both sides
//     agree, that's as close to a certain match as this gets — it's an
//     industry identifier, not a guess.
//  2. ISRC (bridged) — for tracks that didn't match tier 1, look the
//     Spotify track's ISRC up via the MusicBrainz API (which recording(s)
//     that ISRC belongs to) and check those against local files' embedded
//     MusicBrainz Recording IDs. Still ISRC-certain, just resolved in this
//     direction instead of read straight off a tag. This only runs for
//     tracks that need it, not the whole library — see Resolver.
//  3. Fuzzy — for anything still unmatched, a normalized artist/title
//     similarity score against the whole library. Optional, thresholded,
//     and clearly reported as lower-confidence.
//
// Anything that clears none of these is reported as missing.
package match

import (
	"context"
	"strings"

	"spotuify/internal/library"
	"spotuify/internal/spotifyapi"
)

type Method string

const (
	MethodISRC  Method = "isrc"
	MethodFuzzy Method = "fuzzy"
	MethodNone  Method = "missing"
)

// Result is the outcome of matching one playlist track item.
type Result struct {
	Item       spotifyapi.PlaylistTrackItem
	Method     Method
	LocalPath  string
	Confidence float64 // 1.0 for ISRC, similarity score (0..1) for fuzzy, 0 for none
}

// Resolver bridges a Spotify track's ISRC to MusicBrainz Recording IDs
// that might match a local file — see library.MusicBrainzResolver, the
// concrete implementation. A nil Resolver skips tier 2 entirely.
type Resolver interface {
	RecordingsForISRC(ctx context.Context, isrc string) ([]string, error)
}

// Options controls the optional matching tiers.
type Options struct {
	// Resolver, if non-nil, enables the MusicBrainz ISRC-bridge tier.
	Resolver Resolver

	EnableFuzzy    bool
	FuzzyThreshold float64
}

// DefaultOptions returns sane defaults: fuzzy matching on, requiring a
// fairly high similarity score so weak guesses don't masquerade as
// matches, and no MusicBrainz bridging (opt in via Options.Resolver).
func DefaultOptions() Options {
	return Options{EnableFuzzy: true, FuzzyThreshold: 0.82}
}

// All matches every item in a playlist's track list against idx, in order.
// Each local file is assigned to at most one Spotify track (first come,
// first served, ISRC matches — tagged, then bridged — resolved before any
// fuzzy matches are attempted) so a batch of near-duplicate local files
// can't all silently map to the same track.
//
// bridgeProgress, if non-nil, is called after each MusicBrainz lookup in
// tier 2 with (done, total) — that tier is the only one that touches the
// network, and only for tracks tier 1 couldn't already resolve, so total
// is bounded by this playlist's unmatched count, not the library size.
func All(ctx context.Context, items []spotifyapi.PlaylistTrackItem, idx *library.Index, opts Options, bridgeProgress func(done, total int)) []Result {
	results := make([]Result, len(items))
	used := make(map[string]bool)

	for i, item := range items {
		results[i] = Result{Item: item, Method: MethodNone}
		if item.Track == nil || item.IsLocal {
			continue
		}
		isrc := item.Track.ExternalIDs.ISRC
		if isrc == "" {
			continue
		}
		if t, ok := idx.ByISRC(isrc); ok && !used[t.Path] {
			results[i] = Result{Item: item, Method: MethodISRC, LocalPath: t.Path, Confidence: 1}
			used[t.Path] = true
		}
	}

	if opts.Resolver != nil && idx != nil {
		var pending []int
		for i, r := range results {
			if r.Method == MethodNone && r.Item.Track != nil && r.Item.Track.ExternalIDs.ISRC != "" {
				pending = append(pending, i)
			}
		}
		for n, i := range pending {
			if ctx.Err() != nil {
				break
			}
			isrc := results[i].Item.Track.ExternalIDs.ISRC
			mbids, err := opts.Resolver.RecordingsForISRC(ctx, isrc)
			if err == nil {
				for _, mbid := range mbids {
					if t, ok := idx.ByMBID(mbid); ok && !used[t.Path] {
						results[i].Method = MethodISRC
						results[i].LocalPath = t.Path
						results[i].Confidence = 1
						used[t.Path] = true
						break
					}
				}
			}
			if bridgeProgress != nil {
				bridgeProgress(n+1, len(pending))
			}
		}
	}

	if opts.EnableFuzzy && idx != nil {
		for i := range results {
			if results[i].Method != MethodNone || results[i].Item.Track == nil {
				continue
			}
			best, bestScore := bestFuzzyMatch(results[i].Item.Track, idx.Tracks, used)
			if best != nil && bestScore >= opts.FuzzyThreshold {
				results[i].Method = MethodFuzzy
				results[i].LocalPath = best.Path
				results[i].Confidence = bestScore
				used[best.Path] = true
			}
		}
	}

	return results
}

func bestFuzzyMatch(st *spotifyapi.Track, candidates []library.Track, used map[string]bool) (*library.Track, float64) {
	var best *library.Track
	bestScore := 0.0
	for i := range candidates {
		t := &candidates[i]
		if used[t.Path] {
			continue
		}
		score := trackScore(st, t)
		if score > bestScore {
			bestScore = score
			best = t
		}
	}
	return best, bestScore
}

func trackScore(st *spotifyapi.Track, lt *library.Track) float64 {
	titleScore := similarity(st.Name, lt.Title)
	artistScore := similarity(joinArtists(st.Artists), lt.Artist)
	return titleScore*0.65 + artistScore*0.35
}

func joinArtists(artists []spotifyapi.Artist) string {
	names := make([]string, len(artists))
	for i, a := range artists {
		names[i] = a.Name
	}
	return strings.Join(names, " ")
}
