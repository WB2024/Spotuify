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
//
// An ISRC/MBID tag identifies the *recording*, not which local file to
// pick when more than one carries it — a various-artists compilation can
// legitimately reuse the same official master as the original album, and
// a reissue often just carries the same tag forward. Tiers 1 and 2 both
// resolve that through chooseCandidate: rank whatever's tagged by how well
// its album matches Spotify's, and if even the best of those looks like a
// poor match, also check the rest of the library for an untagged file
// that's unmistakably the same song (title and artist both near-exact)
// under a clearly better-matching album — see chooseCandidate's own doc
// comment for the concrete case this is for.
package match

import (
	"context"
	"strings"

	"spotuify/internal/library"
	"spotuify/internal/spotifyapi"
)

type Method string

const (
	MethodISRC   Method = "isrc"
	MethodFuzzy  Method = "fuzzy"
	MethodManual Method = "manual" // user picked the file explicitly, overriding whatever (if anything) matched automatically
	MethodNone   Method = "missing"
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

	// ManualOverrides is a Spotify track ID -> local file path table
	// (config.ManualMatches.Snapshot()) checked before every other tier —
	// a previous manual correction always wins over whatever automatic
	// matching would find this run, so re-matching a playlist later (to
	// pick up newly-added local files, say) doesn't silently recompute a
	// deliberate correction back to "missing". An override whose file no
	// longer exists in idx is skipped, falling through to normal matching,
	// rather than pointing the .m3u8 at a file that's gone.
	ManualOverrides map[string]string
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

		if path, ok := opts.ManualOverrides[item.Track.ID]; ok && !used[path] {
			if _, found := idx.ByPath(path); found {
				results[i] = Result{Item: item, Method: MethodManual, LocalPath: path, Confidence: 1}
				used[path] = true
				continue
			}
		}

		isrc := item.Track.ExternalIDs.ISRC
		if isrc == "" {
			continue
		}
		if t := chooseCandidate(item.Track, idx.ByISRC(isrc), idx, used); t != nil {
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
				var candidates []*library.Track
				for _, mbid := range mbids {
					candidates = append(candidates, idx.ByMBID(mbid)...)
				}
				if t := chooseCandidate(results[i].Item.Track, candidates, idx, used); t != nil {
					results[i].Method = MethodISRC
					results[i].LocalPath = t.Path
					results[i].Confidence = 1
					used[t.Path] = true
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

// albumMatchGate is the minimum AlbumSimilarity an ISRC/MBID-tagged
// candidate needs before its match is trusted without a second look.
// Below it, betterAlbumAlternative checks whether an untagged file is a
// clearly better fit — chooseCandidate's doc comment has the concrete case
// this is for. 0.5 is deliberately loose: a legitimately-worded album
// match (different capitalization, "&" vs "and", a missing "The") should
// still comfortably clear it without triggering the extra search; it's
// aimed at catching near-zero-overlap cases like an unrelated compilation
// title, not nitpicking phrasing.
const albumMatchGate = 0.5

// altTitleThreshold and altArtistThreshold gate betterAlbumAlternative's
// library-wide search: high enough that only a file unmistakably *the
// same recording* — not just a same-named cover or a different song by
// the same artist — is ever considered as a replacement for a tagged
// candidate.
const (
	altTitleThreshold  = 0.90
	altArtistThreshold = 0.7
)

// chooseCandidate picks the best local match for st among candidates
// already confirmed, via ISRC or a MusicBrainz-bridged MBID, to be the
// same recording — ranked by how well each one's *album* matches st's, so
// when the same recording sits in more than one local file (a plain
// pressing, a remaster, a various-artists compilation that reused the
// same master), the one actually filed under the matching release wins
// rather than whichever the database happened to return first.
//
// Concrete case this fixes: a track tagged with the correct ISRC only on
// a copy filed under an unrelated tribute/compilation album, while the
// copy under the *right* album (a remaster, say) has no ISRC tag of its
// own at all — verified against a real library where "Gimme the Loot" by
// The Notorious B.I.G. existed three times (the original album, a
// remaster, and a 2021 various-artists compilation), only the compilation
// copy carried an ISRC tag, and it happened to be the one Spotify's ISRC
// pointed at. Rather than accept that untrustworthy pairing at face
// value, this notices its album is a poor match, and finds the remaster
// copy instead by requiring a near-exact title/artist match (so it's
// unmistakably the same recording) combined with the best album match.
func chooseCandidate(st *spotifyapi.Track, candidates []*library.Track, idx *library.Index, used map[string]bool) *library.Track {
	if len(candidates) == 0 {
		return nil
	}

	var best *library.Track
	bestScore := -1.0
	for _, c := range candidates {
		if used[c.Path] {
			continue
		}
		if score := AlbumSimilarity(st.Album.Name, c.Album); score > bestScore {
			bestScore = score
			best = c
		}
	}

	if best != nil && bestScore >= albumMatchGate {
		return best
	}
	if alt := betterAlbumAlternative(st, idx, used, bestScore); alt != nil {
		return alt
	}
	return best
}

// betterAlbumAlternative scans the whole library for an untagged file
// that's unmistakably the same recording as st (title and artist both
// near-exact) filed under an album that matches st's better than
// currentScore. Only worth the full scan when chooseCandidate's tagged
// candidates didn't already clear albumMatchGate.
func betterAlbumAlternative(st *spotifyapi.Track, idx *library.Index, used map[string]bool, currentScore float64) *library.Track {
	if idx == nil {
		return nil
	}
	spotifyArtist := joinArtists(st.Artists)

	var best *library.Track
	bestScore := currentScore
	for i := range idx.Tracks {
		c := &idx.Tracks[i]
		if used[c.Path] {
			continue
		}
		if similarity(st.Name, c.Title) < altTitleThreshold {
			continue
		}
		if similarity(spotifyArtist, c.Artist) < altArtistThreshold {
			continue
		}
		if score := AlbumSimilarity(st.Album.Name, c.Album); score > bestScore {
			bestScore = score
			best = c
		}
	}
	return best
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
