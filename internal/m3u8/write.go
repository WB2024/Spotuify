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

// Write renders match results for one playlist into <dir>/<playlist name>/:
// a "<playlist name>.m3u8" with every matched track (in playlist order,
// paths relative to that folder), a "missing.txt" if anything is
// unmatched, and the playlist's cover art — matching Navidrome's own
// convention for a file-backed playlist folder, so it scans in cleanly
// (Navidrome auto-discovers .m3u8 files anywhere in its music folder and
// registers/updates them as playlists on its own scan cycle; the display
// name it uses comes from the #PLAYLIST directive written into the file
// below, not the folder or file name). downloadCover controls whether the
// cover art step runs at all.
func Write(ctx context.Context, httpClient *http.Client, dir string, playlist *spotifyapi.FullPlaylist, results []match.Result, downloadCover bool) (*Result, error) {
	name := sanitizeFilename(playlist.Name)
	outDir := filepath.Join(dir, name)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating playlist directory: %w", err)
	}

	res := &Result{Dir: outDir}

	m3u8Path := filepath.Join(outDir, name+".m3u8")
	if err := writeM3U8(m3u8Path, outDir, playlist.Name, results, res); err != nil {
		return nil, fmt.Errorf("writing %s.m3u8: %w", name, err)
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

// navidromeGroup is the folder Navidrome/Feishin group Spotuify-generated
// playlists under in their playlist sidebar — Navidrome treats "/" in a
// #PLAYLIST directive as a UI folder hierarchy, not a filesystem path.
// Matches this user's existing "Spotify/SR/<name>" convention so old and
// new playlists sit in the same place.
const navidromeGroup = "Spotify/SR/"

func writeM3U8(path, outDir, playlistName string, results []match.Result, res *Result) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#PLAYLIST:" + navidromeGroup + playlistName + "\n")

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

// unsafeFilenameChars covers what's actually illegal (or awkward) in a
// file/directory name — just "/" on Linux, plus the extra characters
// Windows reserves, in case this output ever gets synced to one.
var unsafeFilenameChars = regexp.MustCompile(`[/\\:*?"<>|]+`)

// sanitizeFilename makes name safe to use as a file/directory name while
// keeping it human-readable — unlike slugify (used for the JSON/CSV
// export), this deliberately preserves case, spacing, and punctuation, so
// a playlist named "Magnum Opus" produces a folder/file named "Magnum
// Opus", matching the convention these playlists are already stored in.
func sanitizeFilename(name string) string {
	s := strings.TrimSpace(name)
	s = unsafeFilenameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, " .")
	if s == "" {
		s = "Playlist"
	}
	if len(s) > 150 {
		s = strings.TrimSpace(s[:150])
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
