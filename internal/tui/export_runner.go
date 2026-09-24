package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/export"
	"spotuify/internal/spotifyapi"
)

// runExport fetches full details and every track for each playlist in
// queue, exports it, and reports progress over ch. It's run in its own
// goroutine; ch is closed when done.
func runExport(ctx context.Context, client *spotifyapi.Client, exporter *export.Exporter, queue []spotifyapi.SimplifiedPlaylist, ch chan<- exportEvent) {
	defer close(ch)

	for i, sp := range queue {
		if ctx.Err() != nil {
			return
		}

		send(ctx, ch, exportEvent{kind: eventStatus, text: fmt.Sprintf("%s: fetching playlist details...", sp.Name)})

		full, err := client.Playlist(ctx, sp.ID)
		if err != nil {
			send(ctx, ch, exportEvent{kind: eventPlaylistDone, playlistName: sp.Name, err: err})
			if _, locked := spotifyapi.AsRateLimitError(err); locked {
				skipRest(ctx, ch, queue[i+1:], err)
				return
			}
			continue
		}

		tracks, err := client.PlaylistTracks(ctx, sp.ID, func(fetched, total int) {
			send(ctx, ch, exportEvent{kind: eventStatus, text: fmt.Sprintf("%s: fetched %d/%d tracks", sp.Name, fetched, total)})
		})
		if err != nil {
			send(ctx, ch, exportEvent{kind: eventPlaylistDone, playlistName: sp.Name, err: err})
			if _, locked := spotifyapi.AsRateLimitError(err); locked {
				skipRest(ctx, ch, queue[i+1:], err)
				return
			}
			continue
		}

		send(ctx, ch, exportEvent{kind: eventStatus, text: fmt.Sprintf("%s: writing JSON, CSV, and cover art...", sp.Name)})

		res, err := exporter.Export(ctx, full, tracks)
		if err != nil {
			send(ctx, ch, exportEvent{kind: eventPlaylistDone, playlistName: sp.Name, err: err})
			continue
		}

		send(ctx, ch, exportEvent{
			kind:         eventPlaylistDone,
			playlistName: sp.Name,
			trackCount:   res.TrackCount,
			coverPath:    res.CoverPath,
			dir:          res.Dir,
		})
	}

	send(ctx, ch, exportEvent{kind: eventAllDone})
}

// skipRest marks every not-yet-attempted playlist in the batch with the
// same rate-limit error that just stopped it, without making any more
// requests - every one of them would hit the exact same lockout, so
// there's nothing to gain by trying each in turn and a real cost (more
// load on an API that just said to back off).
func skipRest(ctx context.Context, ch chan<- exportEvent, rest []spotifyapi.SimplifiedPlaylist, err error) {
	for _, sp := range rest {
		send(ctx, ch, exportEvent{kind: eventPlaylistDone, playlistName: sp.Name, err: err})
	}
}

func send(ctx context.Context, ch chan<- exportEvent, ev exportEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// waitForExportEvent returns a tea.Cmd that blocks for the next event on
// ch. The Update loop re-issues this after every event to keep pumping
// messages until the channel closes.
func waitForExportEvent(ch <-chan exportEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return exportEvent{kind: eventAllDone}
		}
		return ev
	}
}
