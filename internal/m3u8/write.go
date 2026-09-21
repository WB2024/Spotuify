// Package m3u8 writes match results out as an M3U8 playlist (paths
// relative to the playlist file, per the convention every major player —
// VLC, foobar2000, Kodi, Plex — expects for a portable playlist), a
// human-readable report of anything that couldn't be matched, and the
// playlist's cover art — one folder per playlist, mirroring the layout
// internal/export uses for the JSON/CSV export.
package m3u8

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"spotuify/internal/coverart"
	"spotuify/internal/match"
	"spotuify/internal/spotifyapi"
)

// Result describes what was written for one playlist.
type Result struct {
	Dir          string
	M3U8Path     string
	MissingPath  string // empty if nothing was missing
	CoverPath    string // empty if no cover art was available/downloaded
	Matched      int
	MatchedISRC  int
	MatchedFuzzy int
	Missing      int
}

// Write renders match results for one playlist into
// <dir>/<playlist-slug>-<id>/: an .m3u8 with every matched track (in
// playlist order, paths relative to that folder), a "missing.txt" if
// anything is unmatched, and the playlist's cover art. downloadCover
// controls whether the cover art step runs at all.
func Write(ctx context.Context, httpClient *http.Client, dir string, playlist *spotifyapi.FullPlaylist, results []match.Result, downloadCover bool) (*Result, error) {
	outDir := filepath.Join(dir, fmt.Sprintf("%s-%s", slugify(playlist.Name), playlist.ID))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating playlist directory: %w", err)
	}

	res := &Result{Dir: outDir}

	m3u8Path := filepath.Join(outDir, "playlist.m3u8")
	if err := writeM3U8(m3u8Path, outDir, playlist.Name, results, res); err != nil {
		return nil, fmt.Errorf("writing playlist.m3u8: %w", err)
	}
	res.M3U8Path = m3u8Path

	if res.Missing > 0 {
		missingPath := filepath.Join(outDir, "missing.txt")
		if err := writeMissing(missingPath, playlist.Name, results); err != nil {
			return nil, fmt.Errorf("writing missing.txt: %w", err)
		}
		res.MissingPath = missingPath
	}

	if downloadCover {
		coverPath, err := coverart.Download(ctx, httpClient, playlist.Images, outDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not download cover art for %q: %v\n", playlist.Name, err)
		} else {
			res.CoverPath = coverPath
		}
	}

	return res, nil
}

func writeM3U8(path, outDir, playlistName string, results []match.Result, res *Result) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#PLAYLIST:" + playlistName + "\n")

	for _, r := range results {
		if r.Item.Track == nil {
			continue
		}
		switch r.Method {
		case match.MethodISRC:
			res.MatchedISRC++
		case match.MethodFuzzy:
			res.MatchedFuzzy++
		default:
			res.Missing++
			continue
		}
		res.Matched++

		rel, err := filepath.Rel(outDir, r.LocalPath)
		if err != nil {
			rel = r.LocalPath // different volumes etc: fall back to absolute
		}

		seconds := r.Item.Track.DurationMs / 1000
		fmt.Fprintf(&b, "#EXTINF:%d,%s - %s\n", seconds, artistNames(r.Item.Track), r.Item.Track.Name)
		b.WriteString(filepath.ToSlash(rel) + "\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeMissing(path, playlistName string, results []match.Result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Tracks from %q not found in your local library\n", playlistName)
	b.WriteString(strings.Repeat("=", 60) + "\n\n")

	for _, r := range results {
		if r.Method != match.MethodNone || r.Item.Track == nil {
			continue
		}
		t := r.Item.Track
		fmt.Fprintf(&b, "%s - %s\n", artistNames(t), t.Name)
		if t.Album.Name != "" {
			fmt.Fprintf(&b, "    Album: %s\n", t.Album.Name)
		}
		if t.ExternalURLs.Spotify != "" {
			fmt.Fprintf(&b, "    Spotify: %s\n", t.ExternalURLs.Spotify)
		}
		b.WriteString("\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

var unsafeFilename = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = unsafeFilename.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "playlist"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func artistNames(t *spotifyapi.Track) string {
	names := make([]string, len(t.Artists))
	for i, a := range t.Artists {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}
