package match

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"spotuify/internal/library"
)

var reportCSVHeader = []string{
	"position", "track_name", "artists", "album", "isrc",
	"method", "confidence", "musicbrainz_recording_id", "matched_path",
	"spotify_track_id",
}

// WriteReport writes match-report.csv into dir: one row per track, with
// exactly what decided its outcome (ISRC, method, confidence) and either
// the local file it matched - including that file's own embedded
// MusicBrainz Recording ID tag, via idx, not a network lookup - or an
// explicit MISSING marker.
//
// This is a report, not a cache: nothing here is read back by the matcher.
// MusicBrainz lookups are already cached indefinitely and globally, keyed
// by ISRC (see library.MusicBrainzResolver) - re-matching a playlist,
// including one that's already fully matched, never repeats a lookup this
// report could have saved. The point of this file is to make a run's
// reasoning inspectable after the fact (why is this track still missing,
// what did it actually match to) without needing to reopen the app.
func WriteReport(dir string, results []Result, idx *library.Index) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "match-report.csv"))
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write(reportCSVHeader); err != nil {
		return err
	}

	pos := 0
	for _, r := range results {
		t := r.Item.Track
		if t == nil { // a local file already in the playlist, not a Spotify catalog track - nothing to report
			continue
		}
		pos++

		names := make([]string, len(t.Artists))
		for i, a := range t.Artists {
			names[i] = a.Name
		}

		matchedPath := r.LocalPath
		mbid := ""
		if matchedPath != "" {
			if lt, ok := idx.ByPath(matchedPath); ok {
				mbid = lt.MBID
			}
		} else {
			matchedPath = "MISSING"
		}

		row := []string{
			strconv.Itoa(pos),
			t.Name,
			strings.Join(names, "; "),
			t.Album.Name,
			t.ExternalIDs.ISRC,
			string(r.Method),
			strconv.FormatFloat(r.Confidence, 'f', 2, 64),
			mbid,
			matchedPath,
			t.ID,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return w.Error()
}
