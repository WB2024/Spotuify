// Package lidarrmatch turns a Spotify track into the Lidarr album it
// belongs to, and applies the user's chosen action (add, monitor, search)
// to it.
//
// Spotify identifies an album only by name, and Lidarr's free-text lookup
// is loose (it matches artist and album names independently, and ranks
// the artist's other releases highly), so the primary route goes through
// MusicBrainz instead: the track's ISRC → its MusicBrainz recording(s) →
// the release groups those appear on → the one whose title matches the
// Spotify album → Lidarr's exact "lidarr:<release-group id>" lookup. That
// lands on the right album even when text search buries it. Free-text
// lookup is the fallback for tracks MusicBrainz doesn't know.
package lidarrmatch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"spotuify/internal/library"
	"spotuify/internal/lidarrapi"
	"spotuify/internal/match"
	"spotuify/internal/spotifyapi"
)

// Track is what's needed from a Spotify track to find its album.
type Track struct {
	Artist string // primary artist
	Title  string
	Album  string
	ISRC   string
}

// TrackFromSpotify extracts a Track from a Spotify track object.
func TrackFromSpotify(t *spotifyapi.Track) Track {
	tr := Track{Title: t.Name, Album: t.Album.Name, ISRC: t.ExternalIDs.ISRC}
	if len(t.Artists) > 0 {
		tr.Artist = t.Artists[0].Name
	}
	return tr
}

// Candidate is one possible Lidarr album for a track, with how confident
// the resolver is that it's the right one.
type Candidate struct {
	Album lidarrapi.Album
	Score float64
	Via   string // "musicbrainz" (exact ID match) or "search" (free text)
}

// Resolver finds Lidarr album candidates. MusicBrainz may be nil, in which
// case only free-text lookup is used.
type Resolver struct {
	Lidarr      *lidarrapi.Client
	MusicBrainz *library.MusicBrainzResolver
}

const (
	maxRecordings   = 3   // per ISRC: recordings past the first few are re-releases with identical release groups
	maxMBCandidates = 4   // release groups to look up in Lidarr per track
	minMBScore      = 0.5 // below this a release group is probably a compilation reusing the track
)

// Candidates returns ranked Lidarr albums the track could belong to, best
// first. An empty result with a nil error means nothing plausible was
// found anywhere.
func (r *Resolver) Candidates(ctx context.Context, t Track) ([]Candidate, error) {
	var out []Candidate
	seen := map[string]bool{}
	add := func(c Candidate) {
		if c.Album.ForeignAlbumID == "" || seen[c.Album.ForeignAlbumID] {
			return
		}
		seen[c.Album.ForeignAlbumID] = true
		out = append(out, c)
	}

	if r.MusicBrainz != nil && t.ISRC != "" {
		groups, err := r.releaseGroups(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			album, err := r.Lidarr.LookupAlbumByMBID(ctx, g.ID)
			if err != nil {
				return nil, err
			}
			if album != nil {
				add(Candidate{Album: *album, Score: g.score, Via: "musicbrainz"})
			}
		}
	}

	if len(out) == 0 {
		found, err := r.searchCandidates(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, c := range found {
			add(c)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// Confident reports whether a candidate is safe to act on without a human
// looking at it — what the bulk "add all missing" action requires before
// it adds anything.
func Confident(c Candidate) bool {
	if c.Via == "musicbrainz" {
		// The recording is definitely on this release group; the score
		// says whether the release group is the album Spotify named, or
		// just a compilation that reused the track.
		return c.Score >= 0.6
	}
	return c.Score >= 0.85
}

type scoredGroup struct {
	library.ReleaseGroup
	score float64
}

// releaseGroups resolves the track's ISRC through MusicBrainz to the
// release groups it appears on, scored by how well each one's title
// matches the Spotify album name. Compilations/soundtracks reusing the
// track are penalised unless that's demonstrably what the Spotify album
// is too.
func (r *Resolver) releaseGroups(ctx context.Context, t Track) ([]scoredGroup, error) {
	recordings, err := r.MusicBrainz.RecordingsForISRC(ctx, t.ISRC)
	if err != nil {
		return nil, fmt.Errorf("MusicBrainz: %w", err)
	}
	if len(recordings) > maxRecordings {
		recordings = recordings[:maxRecordings]
	}

	seen := map[string]bool{}
	var groups []scoredGroup
	for _, rec := range recordings {
		rgs, err := r.MusicBrainz.ReleaseGroupsForRecording(ctx, rec)
		if err != nil {
			return nil, fmt.Errorf("MusicBrainz: %w", err)
		}
		for _, g := range rgs {
			if seen[g.ID] {
				continue
			}
			seen[g.ID] = true
			score := match.Similarity(t.Album, g.Title)
			if isCompilation(g) && score < 0.85 && !looksLikeCompilation(t.Album) {
				score -= 0.25
			}
			groups = append(groups, scoredGroup{ReleaseGroup: g, score: score})
		}
	}

	sort.SliceStable(groups, func(i, j int) bool { return groups[i].score > groups[j].score })

	var keep []scoredGroup
	for _, g := range groups {
		if g.score < minMBScore && len(keep) > 0 {
			break
		}
		keep = append(keep, g)
		if len(keep) == maxMBCandidates {
			break
		}
	}
	return keep, nil
}

// looksLikeCompilation reports whether a Spotify album name is itself a
// compilation ("Greatest Hits", "The Best of ..."), in which case
// MusicBrainz compilations are exactly what should match it.
func looksLikeCompilation(album string) bool {
	a := strings.ToLower(album)
	for _, marker := range []string{"greatest hits", "best of", "anthology", "collection", "essential", "the hits", "singles"} {
		if strings.Contains(a, marker) {
			return true
		}
	}
	return false
}

func isCompilation(g library.ReleaseGroup) bool {
	for _, s := range g.SecondaryTypes {
		switch strings.ToLower(s) {
		case "compilation", "soundtrack", "dj-mix", "mixtape/street":
			return true
		}
	}
	return false
}

// searchCandidates is the free-text fallback: Lidarr's lookup for
// "<artist> <album>", filtered to results whose artist actually resembles
// the track's, scored on both names.
func (r *Resolver) searchCandidates(ctx context.Context, t Track) ([]Candidate, error) {
	term := strings.TrimSpace(t.Artist + " " + cleanAlbumName(t.Album))
	if term == "" {
		return nil, nil
	}
	albums, err := r.Lidarr.LookupAlbums(ctx, term)
	if err != nil {
		return nil, err
	}

	var out []Candidate
	for _, a := range albums {
		artistScore := match.Similarity(t.Artist, a.Artist.ArtistName)
		if artistScore < 0.6 {
			continue
		}
		albumScore := match.Similarity(t.Album, a.Title)
		out = append(out, Candidate{Album: a, Score: 0.6*albumScore + 0.4*artistScore, Via: "search"})
		if len(out) == 8 {
			break
		}
	}
	return out, nil
}

// cleanAlbumName drops the edition/remaster qualifiers Spotify appends to
// album names ("(Special Edition)", "[2014 Remaster]") that only hurt a
// text search.
func cleanAlbumName(s string) string {
	for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}} {
		for {
			i := strings.Index(s, pair[0])
			if i < 0 {
				break
			}
			j := strings.Index(s[i:], pair[1])
			if j < 0 {
				break
			}
			s = s[:i] + s[i+j+1:]
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

// Mode is what to do once an album is in Lidarr and monitored.
type Mode int

const (
	AddOnly      Mode = iota // leave it monitored for Lidarr's own schedule
	AddAndSearch             // also queue an immediate search
)

func (m Mode) String() string {
	if m == AddAndSearch {
		return "Add + search"
	}
	return "Add only"
}

// Apply makes Lidarr want album, doing whichever of add / monitor / search
// its current state calls for, and returns a short description of what
// actually happened (for the results screen).
func Apply(ctx context.Context, client *lidarrapi.Client, album lidarrapi.Album, opts lidarrapi.AddOptions, mode Mode) (string, error) {
	search := mode == AddAndSearch

	switch {
	case !album.InLidarr():
		opts.SearchForNewAlbum = search
		if _, err := client.AddAlbum(ctx, album, opts); err != nil {
			return "", err
		}
		if search {
			return "added to Lidarr and searching", nil
		}
		return "added to Lidarr", nil

	case !album.Monitored:
		if err := client.MonitorAlbums(ctx, []int{album.ID}, true); err != nil {
			return "", err
		}
		if search {
			if err := client.SearchAlbums(ctx, []int{album.ID}); err != nil {
				return "", err
			}
			return "now monitored in Lidarr and searching", nil
		}
		return "now monitored in Lidarr", nil

	default:
		if search {
			if err := client.SearchAlbums(ctx, []int{album.ID}); err != nil {
				return "", err
			}
			return "already monitored — search queued", nil
		}
		return "already monitored in Lidarr", nil
	}
}
