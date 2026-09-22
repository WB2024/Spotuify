package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ManualMatches is a small on-disk override table, keyed by Spotify track
// ID, for tracks the user has picked a local file for by hand from the
// match results screen — so a manual correction survives re-running the
// match later (to pick up newly-added local files, say), instead of
// getting silently recomputed away: without this, a from-scratch rerun has
// no memory of the correction at all, and a track that used to report
// missing goes right back to reporting missing. Safe for the zero value
// (Get/Set on a nil pointer behave as "no overrides set").
type ManualMatches struct {
	path      string
	overrides map[string]string
}

// LoadManualMatches reads the override table from path. It always returns
// a usable table, even on error (a missing file, or one that fails to
// parse) — callers that don't care about the specific reason can safely
// ignore the error and use the result as-is (just starting with no
// overrides), since future Set calls still know where to save.
func LoadManualMatches(path string) (*ManualMatches, error) {
	mm := &ManualMatches{path: path, overrides: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mm, nil
		}
		return mm, err
	}
	if len(data) == 0 {
		return mm, nil
	}
	if err := json.Unmarshal(data, &mm.overrides); err != nil {
		return mm, err
	}
	return mm, nil
}

// Get returns the local file path picked for the given Spotify track ID,
// if any.
func (mm *ManualMatches) Get(trackID string) (string, bool) {
	if mm == nil {
		return "", false
	}
	p, ok := mm.overrides[trackID]
	return p, ok
}

// Set records localPath as the correction for trackID and persists the
// table immediately; an empty path clears the override.
func (mm *ManualMatches) Set(trackID, localPath string) error {
	if mm.overrides == nil {
		mm.overrides = map[string]string{}
	}
	if localPath == "" {
		delete(mm.overrides, trackID)
	} else {
		mm.overrides[trackID] = localPath
	}
	return mm.save()
}

// Snapshot returns a plain copy of the override table (Spotify track ID ->
// local file path), for handing to match.Options.ManualOverrides — the
// match package doesn't depend on config, so it takes the plain map rather
// than a *ManualMatches.
func (mm *ManualMatches) Snapshot() map[string]string {
	if mm == nil || len(mm.overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(mm.overrides))
	for k, v := range mm.overrides {
		out[k] = v
	}
	return out
}

func (mm *ManualMatches) save() error {
	if err := os.MkdirAll(filepath.Dir(mm.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(mm.overrides, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mm.path, data, 0o600)
}
