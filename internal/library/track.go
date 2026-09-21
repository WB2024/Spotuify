// Package library builds a matchable index of a local music collection by
// reading it straight out of a Navidrome server's SQLite database, rather
// than re-scanning and re-tagging files Navidrome has already indexed.
package library

// Track is one local audio file's indexed metadata, sourced from
// Navidrome's media_file table.
type Track struct {
	Path       string `json:"path"` // absolute path on this machine
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	DurationMs int    `json:"duration_ms"`

	// MBID is the MusicBrainz Recording ID Navidrome extracted from the
	// file's own tags.
	MBID string `json:"mbid,omitempty"`

	// ISRC is the track's primary International Standard Recording Code:
	// either read by Navidrome from the file's own tags, or — if the file
	// only has an MBID — resolved via the MusicBrainz API and cached. This
	// is the bridge to Spotify, which reports ISRC on every track.
	ISRC string `json:"isrc,omitempty"`

	// ISRCAll holds every ISRC associated with the recording (a track can
	// have several across different releases/reissues); ISRC is ISRCAll[0]
	// when non-empty. Matching checks all of them.
	ISRCAll []string `json:"isrc_all,omitempty"`
}
