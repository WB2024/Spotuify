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
}

// ByISRC returns the local track carrying the given ISRC, if any.
func (idx *Index) ByISRC(isrc string) (*Track, bool) {
	if idx == nil || isrc == "" {
		return nil, false
	}
	t, ok := idx.byISRC[isrc]
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

	CachePath              string
	ResolveMusicBrainzISRC bool
}

// Phase identifies which stage of a load is in progress, for progress
// reporting.
type Phase string

const (
	PhaseReadingDB Phase = "reading Navidrome library"
	PhaseResolving Phase = "resolving MusicBrainz IDs"
)

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

// Load reads every non-missing track from Navidrome's media_file table,
// resolves ISRCs for MusicBrainz-tagged tracks that don't already carry one
// (rate-limited and cached — see musicbrainz.go), and returns a
// ready-to-use Index. It's cancelable via ctx; on cancellation, whatever
// MusicBrainz resolution completed so far is still cached to disk.
func Load(ctx context.Context, cfg LoadConfig, progress func(Progress)) (*Index, error) {
	report := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}

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

	var raw []rawTrack

	for rows.Next() {
		var relPath, title, artist, album, mbid, tagsJSON string
		var duration float64
		if err := rows.Scan(&relPath, &title, &artist, &album, &duration, &mbid, &tagsJSON); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading Navidrome row: %w", err)
		}
		raw = append(raw, rawTrack{
			track: Track{
				Path:       filepath.Join(cfg.MusicPath, filepath.FromSlash(relPath)),
				Title:      title,
				Artist:     artist,
				Album:      album,
				DurationMs: int(duration * 1000),
				MBID:       mbid,
			},
			tags: tagsJSON,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("reading Navidrome rows: %w", err)
	}
	rows.Close()

	report(Progress{Phase: PhaseReadingDB, Done: len(raw), Total: len(raw)})

	for i := range raw {
		if isrcs := parseISRCTag(raw[i].tags); len(isrcs) > 0 {
			raw[i].track.ISRC = isrcs[0]
			raw[i].track.ISRCAll = isrcs
		}
	}

	mbc := loadMBCache(cfg.CachePath)
	if cfg.ResolveMusicBrainzISRC {
		if err := resolveISRCs(ctx, cfg.CachePath, &mbc, pendingMBIDs(raw), report); err != nil {
			return nil, err
		}
		for i := range raw {
			t := &raw[i].track
			if t.ISRC != "" || t.MBID == "" {
				continue
			}
			if e, ok := mbc.Entries[t.MBID]; ok && len(e.ISRCs) > 0 {
				t.ISRC = e.ISRCs[0]
				t.ISRCAll = e.ISRCs
			}
		}
	}

	tracks := make([]Track, len(raw))
	for i := range raw {
		tracks[i] = raw[i].track
	}
	return buildIndex(tracks), nil
}

// rawTrack pairs a partially-built Track with its still-unparsed tags JSON.
type rawTrack struct {
	track Track
	tags  string
}

func pendingMBIDs(raw []rawTrack) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range raw {
		if r.track.MBID != "" && r.track.ISRC == "" && !seen[r.track.MBID] {
			seen[r.track.MBID] = true
			out = append(out, r.track.MBID)
		}
	}
	return out
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

func resolveISRCs(ctx context.Context, cachePath string, cache *mbCache, mbids []string, report func(Progress)) error {
	var pending []string
	for _, mbid := range mbids {
		if e, ok := cache.Entries[mbid]; !ok || !e.Resolved {
			pending = append(pending, mbid)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	client := newMusicBrainzClient()
	resolvedSinceFlush := 0

	for i, mbid := range pending {
		if err := ctx.Err(); err != nil {
			_ = saveMBCache(cachePath, *cache)
			return err
		}

		isrcs, err := client.ResolveISRCs(ctx, mbid)
		if err != nil {
			if ctx.Err() != nil {
				_ = saveMBCache(cachePath, *cache)
				return ctx.Err()
			}
			// Network/API hiccup: leave unresolved so it's retried next
			// load, and move on rather than aborting the whole run.
			report(Progress{Phase: PhaseResolving, Done: i + 1, Total: len(pending)})
			continue
		}

		cache.Entries[mbid] = mbCacheEntry{ISRCs: isrcs, Resolved: true}

		resolvedSinceFlush++
		if resolvedSinceFlush >= 20 {
			_ = saveMBCache(cachePath, *cache)
			resolvedSinceFlush = 0
		}

		report(Progress{Phase: PhaseResolving, Done: i + 1, Total: len(pending)})
	}

	return saveMBCache(cachePath, *cache)
}

func buildIndex(tracks []Track) *Index {
	idx := &Index{Tracks: tracks, byISRC: make(map[string]*Track)}
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
	}
	return idx
}
