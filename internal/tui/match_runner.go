package tui

import (
	"context"
	"fmt"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
	"spotuify/internal/library"
	"spotuify/internal/m3u8"
	"spotuify/internal/match"
	"spotuify/internal/navidromeapi"
	"spotuify/internal/spotifyapi"
)

type matchEventKind int

const (
	matchEventStatus matchEventKind = iota
	matchEventPlaylistDone
	matchEventAllDone
)

// matchEvent is sent from the background matching goroutine to the Update
// loop over a channel — same channel-pump pattern as exportEvent.
type matchEvent struct {
	kind         matchEventKind
	text         string // for matchEventStatus
	playlistName string // for matchEventPlaylistDone
	playlist     *spotifyapi.FullPlaylist
	results      []match.Result
	write        *m3u8.Result
	err          error  // fetch/write failure for this playlist, if any
	syncWarn     string // non-fatal: cover art written locally but not uploaded to Navidrome
}

// runMatch fetches full track listings for each queued playlist, matches
// them against idx, writes the .m3u8 (+ missing report + cover art), and
// reports progress over ch. It's run in its own goroutine; ch is closed
// when done.
func runMatch(ctx context.Context, client *spotifyapi.Client, httpClient *http.Client, idx *library.Index, cfg *config.Config, groups *config.PlaylistGroups, queue []spotifyapi.SimplifiedPlaylist, ch chan<- matchEvent) {
	defer close(ch)

	opts := match.Options{EnableFuzzy: cfg.EnableFuzzyMatching, FuzzyThreshold: match.DefaultOptions().FuzzyThreshold}
	if cfg.ResolveMusicBrainzISRC {
		resolver := library.NewMusicBrainzResolver(cfg.LibraryCachePath)
		defer resolver.Close()
		opts.Resolver = resolver
	}

	var ndClient *navidromeapi.Client
	if cfg.HasNavidromeAPI() {
		ndClient = navidromeapi.New(cfg.NavidromeAPIURL, cfg.NavidromeUsername, cfg.NavidromePassword)
	}

	for _, sp := range queue {
		if ctx.Err() != nil {
			return
		}

		sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: fetching playlist details...", sp.Name)})

		full, err := client.Playlist(ctx, sp.ID)
		if err != nil {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, err: err})
			continue
		}

		tracks, err := client.PlaylistTracks(ctx, sp.ID, func(fetched, total int) {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: fetched %d/%d tracks", sp.Name, fetched, total)})
		})
		if err != nil {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, err: err})
			continue
		}

		sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: matching against local library...", sp.Name)})
		results := match.All(ctx, tracks, idx, opts, func(done, total int) {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: bridging %d/%d unmatched tracks via MusicBrainz...", sp.Name, done, total)})
		})

		sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: writing .m3u8...", sp.Name)})
		writeRes, err := m3u8.Write(ctx, httpClient, cfg.M3U8Dir, full, results, cfg.DownloadCovers, resolveGroup(cfg, groups, sp.ID))
		if err != nil {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, results: results, err: err})
			continue
		}

		var syncWarn string
		if ndClient != nil && (writeRes.CoverPath != "" || full.Description != "") {
			err := syncNavidromeMetadata(ctx, ndClient, cfg, writeRes.M3U8Path, writeRes.CoverPath, full.Description, func(text string) {
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: %s", sp.Name, text)})
			})
			if err != nil {
				syncWarn = err.Error()
			}
		}

		sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, playlist: full, results: results, write: writeRes, syncWarn: syncWarn})
	}

	sendMatchEvent(ctx, ch, matchEvent{kind: matchEventAllDone})
}

// resolveGroup returns the Navidrome group (the #PLAYLIST directive prefix)
// to use for playlistID: its per-playlist override if one is set, else the
// configured global default.
func resolveGroup(cfg *config.Config, groups *config.PlaylistGroups, playlistID string) string {
	if g, ok := groups.Get(playlistID); ok {
		return g
	}
	return cfg.NavidromeGroup
}

func waitForMatchEvent(ch <-chan matchEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return matchEvent{kind: matchEventAllDone}
		}
		return ev
	}
}

func sendMatchEvent(ctx context.Context, ch chan<- matchEvent, ev matchEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}
