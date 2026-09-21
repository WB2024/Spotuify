// Package export writes a fully-fetched Spotify playlist to disk as JSON,
// CSV, and (separately) its highest-resolution cover art.
package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spotuify/internal/coverart"
	"spotuify/internal/spotifyapi"
)

// Result describes what was written for one playlist export.
type Result struct {
	Dir           string
	JSONPath      string
	CSVPath       string
	CoverPath     string // empty if no cover art was available
	TrackCount    int
	LocalTrackCnt int
}

// playlistExport is the shape written to playlist.json: the full playlist
// object plus its complete track listing (which Spotify never returns
// inline for more than one page).
type playlistExport struct {
	ExportedAt time.Time                      `json:"exported_at"`
	Playlist   spotifyapi.FullPlaylist        `json:"playlist"`
	Tracks     []spotifyapi.PlaylistTrackItem `json:"tracks"`
}

// Exporter writes playlist exports under Dir, downloading cover art with
// HTTPClient (a plain, unauthenticated client is fine — Spotify's cover
// art CDN URLs are public) unless DownloadCovers is false.
type Exporter struct {
	Dir            string
	DownloadCovers bool
	HTTPClient     *http.Client
}

func New(dir string, downloadCovers bool, httpClient *http.Client) *Exporter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Exporter{Dir: dir, DownloadCovers: downloadCovers, HTTPClient: httpClient}
}

// Export writes playlist.json, tracks.csv, and (if available) the
// highest-resolution cover art into a per-playlist subdirectory of e.Dir.
func (e *Exporter) Export(ctx context.Context, playlist *spotifyapi.FullPlaylist, tracks []spotifyapi.PlaylistTrackItem) (*Result, error) {
	dirName := fmt.Sprintf("%s-%s", slugify(playlist.Name), playlist.ID)
	outDir := filepath.Join(e.Dir, dirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating export directory: %w", err)
	}

	res := &Result{Dir: outDir, TrackCount: len(tracks)}

	jsonPath := filepath.Join(outDir, "playlist.json")
	if err := writeJSON(jsonPath, playlistExport{
		ExportedAt: time.Now().UTC(),
		Playlist:   *playlist,
		Tracks:     tracks,
	}); err != nil {
		return nil, fmt.Errorf("writing playlist.json: %w", err)
	}
	res.JSONPath = jsonPath

	csvPath := filepath.Join(outDir, "tracks.csv")
	if err := writeCSV(csvPath, playlist, tracks); err != nil {
		return nil, fmt.Errorf("writing tracks.csv: %w", err)
	}
	res.CSVPath = csvPath

	if e.DownloadCovers {
		coverPath, err := coverart.Download(ctx, e.HTTPClient, playlist.Images, outDir)
		if err != nil {
			// Cover art is a nice-to-have; don't fail the whole export over it.
			fmt.Fprintf(os.Stderr, "warning: could not download cover art for %q: %v\n", playlist.Name, err)
		} else {
			res.CoverPath = coverPath
		}
	}

	for _, t := range tracks {
		if t.IsLocal {
			res.LocalTrackCnt++
		}
	}

	return res, nil
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

var csvHeader = []string{
	"position", "added_at", "added_by", "is_local",
	"track_name", "artists", "album", "album_release_date",
	"duration_ms", "duration_mm_ss", "explicit", "popularity",
	"isrc", "track_id", "track_uri", "spotify_url", "preview_url",
}

func writeCSV(path string, playlist *spotifyapi.FullPlaylist, tracks []spotifyapi.PlaylistTrackItem) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write(csvHeader); err != nil {
		return err
	}

	for i, item := range tracks {
		t := item.Track
		if t == nil {
			continue
		}

		artistNames := make([]string, 0, len(t.Artists))
		for _, a := range t.Artists {
			artistNames = append(artistNames, a.Name)
		}

		row := []string{
			strconv.Itoa(i + 1),
			item.AddedAt,
			item.AddedBy.ID,
			strconv.FormatBool(item.IsLocal),
			t.Name,
			strings.Join(artistNames, "; "),
			t.Album.Name,
			t.Album.ReleaseDate,
			strconv.Itoa(t.DurationMs),
			formatDuration(t.DurationMs),
			strconv.FormatBool(t.Explicit),
			strconv.Itoa(t.Popularity),
			t.ExternalIDs.ISRC,
			t.ID,
			t.URI,
			t.ExternalURLs.Spotify,
			t.PreviewURL,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return w.Error()
}

func formatDuration(ms int) string {
	total := ms / 1000
	return fmt.Sprintf("%d:%02d", total/60, total%60)
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
