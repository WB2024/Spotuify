package library

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// mbCacheEntry is what's cached for one MusicBrainz Recording ID. MusicBrainz
// resolution is rate-limited to 1 request/second (see musicbrainz.go), so
// caching it indefinitely — an MBID's ISRCs essentially never change — is
// what keeps repeat library loads fast.
type mbCacheEntry struct {
	ISRCs    []string `json:"isrcs"`
	Resolved bool     `json:"resolved"` // true once looked up, even if it came back empty
}

type mbCache struct {
	Entries map[string]mbCacheEntry `json:"entries"`
}

func loadMBCache(path string) mbCache {
	empty := mbCache{Entries: map[string]mbCacheEntry{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var c mbCache
	if err := json.Unmarshal(b, &c); err != nil || c.Entries == nil {
		return empty
	}
	return c
}

func saveMBCache(path string, c mbCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
