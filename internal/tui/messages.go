package tui

import (
	"net/http"

	"spotuify/internal/spotifyapi"
)

// authDoneMsg carries the result of the (possibly interactive, browser-based)
// Spotify login triggered when the user picks "Export Playlists" without an
// authenticated session yet.
type authDoneMsg struct {
	httpClient *http.Client
	err        error
}

// playlistsLoadedMsg carries the initial data fetch needed to populate the
// playlist list view.
type playlistsLoadedMsg struct {
	user      *spotifyapi.User
	playlists []spotifyapi.SimplifiedPlaylist
}

// errMsg carries a fatal, unrecoverable error (e.g. the initial playlist
// fetch failed).
type errMsg struct{ err error }

// exportEventKind identifies the shape of an exportEvent.
type exportEventKind int

const (
	eventStatus exportEventKind = iota
	eventPlaylistDone
	eventAllDone
)

// exportEvent is sent from the background export goroutine to the Bubble
// Tea update loop over a channel. It doubles as a tea.Msg (any value can
// be one), so no separate wrapper type is needed.
type exportEvent struct {
	kind         exportEventKind
	text         string // for eventStatus
	playlistName string // for eventPlaylistDone
	trackCount   int    // for eventPlaylistDone
	coverPath    string // for eventPlaylistDone
	dir          string // for eventPlaylistDone
	err          error  // for eventPlaylistDone, if that playlist failed
}
