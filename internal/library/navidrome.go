package library

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Index is a loaded local library, ready to be matched against Spotify
// tracks.
type Index struct {
	Tracks []Track
	byISRC map[string]*Track
	byMBID map[string]*Track
}

// ByISRC returns the local track carrying the given ISRC, if any.
func (idx *Index) ByISRC(isrc string) (*Track, bool) {
	if idx == nil || isrc == "" {
		return nil, false
	}
	t, ok := idx.byISRC[isrc]
	return t, ok
}

// ByMBID returns the local track carrying the given MusicBrainz Recording
// ID, if any.
func (idx *Index) ByMBID(mbid string) (*Track, bool) {
	if idx == nil || mbid == "" {
		return nil, false
	}
	t, ok := idx.byMBID[mbid]
	return t, ok
}

// LoadConfig configures a library load from Navidrome.
type LoadConfig struct {
	// NavidromeDBPath is the path to Navidrome's navidrome.db on this
	// machine.
	NavidromeDBPath string

	// MusicPath is this machine's path to the same music folder Navidrome
	// mounts as its library root — i.e. what ND_MUSICFOLDER points to
	// inside Navidrome's own container/environment, translated to a path
	// this process can read. Navidrome stores each track's path relative
	// to that root, so a local track's full path is
	// filepath.Join(MusicPath, media_file.path).
	MusicPath string
}

// Phase identifies which stage of a load is in progress, for progress
// reporting.
type Phase string

const PhaseReadingDB Phase = "reading Navidrome library"

// Progress is reported periodically during Load.
type Progress struct {
	Phase Phase
	Done  int
	Total int
}

// ndTagValue is one entry in a Navidrome tag's value list (tags are
// multi-valued: media_file.tags is a JSON object of tag name -> list of
// {id, value}).
type ndTagValue struct {
	Value string `json:"value"`
}

// Load reads every non-missing track from Navidrome's media_file table and
// returns a ready-to-use Index. This is a plain database read — no
// MusicBrainz API calls happen here (see MusicBrainzResolver for that,
// invoked during matching itself, scoped to just the tracks that need it).
// It's cancelable via ctx.
func Load(ctx context.Context, cfg LoadConfig, progress func(Progress)) (*Index, error) {
	db, cleanup, err := openDB(ctx, cfg.NavidromeDBPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	rows, err := db.QueryContext(ctx,
		`SELECT path, title, artist, album, duration, mbz_recording_id, tags
		 FROM media_file WHERE missing = 0`)
	if err != nil {
		return nil, fmt.Errorf("querying Navidrome database: %w", err)
	}
	defer rows.Close()

	var tracks []Track

	for rows.Next() {
		var relPath, title, artist, album, mbid, tagsJSON string
		var duration float64
		if err := rows.Scan(&relPath, &title, &artist, &album, &duration, &mbid, &tagsJSON); err != nil {
			return nil, fmt.Errorf("reading Navidrome row: %w", err)
		}

		t := Track{
			Path:       filepath.Join(cfg.MusicPath, filepath.FromSlash(relPath)),
			Title:      title,
			Artist:     artist,
			Album:      album,
			DurationMs: int(duration * 1000),
			MBID:       mbid,
		}
		if isrcs := parseISRCTag(tagsJSON); len(isrcs) > 0 {
			t.ISRC = isrcs[0]
			t.ISRCAll = isrcs
		}
		tracks = append(tracks, t)

		if progress != nil && len(tracks)%500 == 0 {
			progress(Progress{Phase: PhaseReadingDB, Done: len(tracks)})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading Navidrome rows: %w", err)
	}

	if progress != nil {
		progress(Progress{Phase: PhaseReadingDB, Done: len(tracks), Total: len(tracks)})
	}

	return buildIndex(tracks), nil
}

func parseISRCTag(tagsJSON string) []string {
	if tagsJSON == "" || tagsJSON == "{}" {
		return nil
	}
	var all map[string][]ndTagValue
	if err := json.Unmarshal([]byte(tagsJSON), &all); err != nil {
		return nil
	}
	vals, ok := all["isrc"]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v.Value != "" {
			out = append(out, v.Value)
		}
	}
	return out
}

func buildIndex(tracks []Track) *Index {
	idx := &Index{Tracks: tracks, byISRC: make(map[string]*Track), byMBID: make(map[string]*Track)}
	for i := range idx.Tracks {
		t := &idx.Tracks[i]

		isrcs := t.ISRCAll
		if len(isrcs) == 0 && t.ISRC != "" {
			isrcs = []string{t.ISRC}
		}
		for _, isrc := range isrcs {
			if isrc == "" {
				continue
			}
			if _, exists := idx.byISRC[isrc]; !exists {
				idx.byISRC[isrc] = t
			}
		}

		if t.MBID != "" {
			if _, exists := idx.byMBID[t.MBID]; !exists {
				idx.byMBID[t.MBID] = t
			}
		}
	}
	return idx
}
