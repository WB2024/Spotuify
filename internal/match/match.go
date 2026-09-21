// Package match maps Spotify playlist tracks to local library files.
//
// Matching is tiered:
//
//  1. ISRC — Spotify reports an ISRC (external_ids.isrc) on nearly every
//     track. The local library index resolves the same identifier either
//     from a file's own ISRC tag, or (see internal/library) via the
//     MusicBrainz API using a file's embedded MusicBrainz Recording ID.
//     Either way, an ISRC match here is as close to certain as matching
//     gets — it's an industry identifier, not a fuzzy guess.
//  2. Fuzzy — for anything left unmatched (no ISRC either side), a
//     normalized artist/title similarity score against the whole library.
//     Optional, thresholded, and clearly reported as lower-confidence.
//
// Anything that clears neither tier is reported as missing.
package match

import (
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

// Options controls the fuzzy fallback tier.
type Options struct {
	EnableFuzzy    bool
	FuzzyThreshold float64
}

// DefaultOptions returns sane defaults: fuzzy matching on, requiring a
// fairly high similarity score so weak guesses don't masquerade as matches.
func DefaultOptions() Options {
	return Options{EnableFuzzy: true, FuzzyThreshold: 0.82}
}

// All matches every item in a playlist's track list against idx, in order.
// Each local file is assigned to at most one Spotify track (first come,
// first served, ISRC matches resolved before any fuzzy matches are
// attempted) so a batch of near-duplicate local files can't all silently
// map to the same track.
func All(items []spotifyapi.PlaylistTrackItem, idx *library.Index, opts Options) []Result {
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
