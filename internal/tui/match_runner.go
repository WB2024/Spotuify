package tui

import (
	"context"
	"fmt"
	"net/http"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
	"spotuify/internal/export"
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
func runMatch(ctx context.Context, client *spotifyapi.Client, httpClient *http.Client, idx *library.Index, cfg *config.Config, groups *config.PlaylistGroups, manualMatches *config.ManualMatches, queue []spotifyapi.SimplifiedPlaylist, ch chan<- matchEvent) {
	defer close(ch)

	opts := match.Options{
		EnableFuzzy:     cfg.EnableFuzzyMatching,
		FuzzyThreshold:  match.DefaultOptions().FuzzyThreshold,
		ManualOverrides: manualMatches.Snapshot(),
	}
	if cfg.ResolveMusicBrainzISRC {
		resolver := library.NewMusicBrainzResolver(cfg.LibraryCachePath)
		defer resolver.Close()
		opts.Resolver = resolver
	}

	var ndClient *navidromeapi.Client
	if cfg.HasNavidromeAPI() {
		ndClient = navidromeapi.New(cfg.NavidromeAPIURL, cfg.NavidromeUsername, cfg.NavidromePassword)
	}

	// Playlists already written by the Export screen (exports/<slug>-<id>/
	// playlist.json) are reused as-is instead of re-fetched from Spotify -
	// scanned once, up front, rather than once per playlist. Trades
	// whatever's changed on Spotify since the export for not spending API
	// calls (or, across enough playlists in one batch, risking rate
	// limiting - see maxRetryAfterWait) on a playlist already fetched in
	// full. Spotify is still used for anything that was never exported.
	exportsByID := export.ScanExports(cfg.ExportDir)

	for i, sp := range queue {
		if ctx.Err() != nil {
			return
		}

		var full *spotifyapi.FullPlaylist
		var tracks []spotifyapi.PlaylistTrackItem

		if dir, ok := exportsByID[sp.ID]; ok {
			if p, t, exportedAt, err := export.LoadExport(dir); err == nil {
				full, tracks = p, t
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: using local export from %s...", sp.Name, exportedAt.Local().Format("2006-01-02 15:04"))})
			}
		}

		if full == nil {
			sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: fetching playlist details...", sp.Name)})

			var err error
			full, err = client.Playlist(ctx, sp.ID)
			if err != nil {
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, err: err})
				if _, locked := spotifyapi.AsRateLimitError(err); locked {
					skipRestMatch(ctx, ch, queue[i+1:], err)
					return
				}
				continue
			}

			tracks, err = client.PlaylistTracks(ctx, sp.ID, func(fetched, total int) {
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: fetched %d/%d tracks", sp.Name, fetched, total)})
			})
			if err != nil {
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, err: err})
				if _, locked := spotifyapi.AsRateLimitError(err); locked {
					skipRestMatch(ctx, ch, queue[i+1:], err)
					return
				}
				continue
			}
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
		hasSyncable := writeRes.CoverPath != "" || full.Description != ""
		switch {
		case ndClient != nil && hasSyncable:
			err := syncNavidromeMetadata(ctx, ndClient, cfg, writeRes.M3U8Path, writeRes.CoverPath, full.Description, func(text string) {
				sendMatchEvent(ctx, ch, matchEvent{kind: matchEventStatus, text: fmt.Sprintf("%s: %s", sp.Name, text)})
			})
			if err != nil {
				syncWarn = err.Error()
			}
		case ndClient == nil && hasSyncable:
			// Otherwise this fails completely silently: the playlist looks
			// done (matched, .m3u8 written) with no indication anywhere
			// that its cover art/description never made it into Navidrome
			// because the API creds in Settings aren't actually set.
			syncWarn = "Navidrome server URL/username/password aren't set in Settings — cover art and description weren't uploaded"
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

// skipRestMatch marks every not-yet-attempted playlist in the batch with
// the same rate-limit error that just stopped it, without making any more
// requests - see skipRest in export_runner.go for why.
func skipRestMatch(ctx context.Context, ch chan<- matchEvent, rest []spotifyapi.SimplifiedPlaylist, err error) {
	for _, sp := range rest {
		sendMatchEvent(ctx, ch, matchEvent{kind: matchEventPlaylistDone, playlistName: sp.Name, err: err})
	}
}

func sendMatchEvent(ctx context.Context, ch chan<- matchEvent, ev matchEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}
