package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PlaylistGroups is a small on-disk override table, keyed by Spotify
// playlist ID, for the few playlists that shouldn't use the global
// NavidromeGroup default. Safe for the zero value (Get/Set on a nil
// pointer behave as "no overrides set").
type PlaylistGroups struct {
	path      string
	overrides map[string]string
}

// LoadPlaylistGroups reads the override table from path. It always returns
// a usable table, even on error (a missing file, or one that fails to
// parse) — callers that don't care about the specific reason can safely
// ignore the error and use the result as-is (just starting with no
// overrides), since future Set calls still know where to save.
func LoadPlaylistGroups(path string) (*PlaylistGroups, error) {
	pg := &PlaylistGroups{path: path, overrides: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return pg, nil
		}
		return pg, err
	}
	if len(data) == 0 {
		return pg, nil
	}
	if err := json.Unmarshal(data, &pg.overrides); err != nil {
		return pg, err
	}
	return pg, nil
}

// Get returns the custom group set for playlistID, if any.
func (pg *PlaylistGroups) Get(playlistID string) (string, bool) {
	if pg == nil {
		return "", false
	}
	g, ok := pg.overrides[playlistID]
	return g, ok
}

// Set records a custom group for playlistID and persists the table
// immediately; an empty group clears the override, reverting that playlist
// back to the global default.
func (pg *PlaylistGroups) Set(playlistID, group string) error {
	if pg.overrides == nil {
		pg.overrides = map[string]string{}
	}
	if group == "" {
		delete(pg.overrides, playlistID)
	} else {
		pg.overrides[playlistID] = group
	}
	return pg.save()
}

func (pg *PlaylistGroups) save() error {
	if err := os.MkdirAll(filepath.Dir(pg.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pg.overrides, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pg.path, data, 0o600)
}
