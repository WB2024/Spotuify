package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spotuify/internal/spotifyapi"
)

// ScanExports lists dir once and returns a map from playlist ID to its
// export subdirectory (<slug>-<id>, see Export), so a batch match run can
// look up each playlist's export in O(1) instead of re-reading dir once
// per playlist. The ID is taken as whatever follows the last "-" in the
// directory name — safe because slugify never emits one at the very end of
// the slug itself, and Spotify playlist IDs are plain base62 with no "-".
// Playlists that were never exported, or whose export moved/was deleted,
// just don't show up in the map; missing dir or empty dir is not an error.
func ScanExports(dir string) map[string]string {
	out := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		i := strings.LastIndexByte(name, '-')
		if i < 0 || i == len(name)-1 {
			continue
		}
		out[name[i+1:]] = filepath.Join(dir, name)
	}
	return out
}

// LoadExport reads a previously-written playlist.json (see Export) back
// into the same types a live Spotify fetch would produce, so a match run
// can reuse an export instead of hitting the API - trading whatever's
// changed on Spotify since ExportedAt for not spending API calls (and, at
// enough playlists in one batch, not risking rate limiting) on a playlist
// that's already been fetched in full.
func LoadExport(dir string) (playlist *spotifyapi.FullPlaylist, tracks []spotifyapi.PlaylistTrackItem, exportedAt time.Time, err error) {
	f, err := os.Open(filepath.Join(dir, "playlist.json"))
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	defer f.Close()

	var pe playlistExport
	if err := json.NewDecoder(f).Decode(&pe); err != nil {
		return nil, nil, time.Time{}, err
	}
	return &pe.Playlist, pe.Tracks, pe.ExportedAt, nil
}
