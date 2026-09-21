package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
	"spotuify/internal/library"
)

// libraryLoadEvent is sent from the background library-load goroutine to
// the Update loop over a channel — mirrors the exportEvent channel-pump
// pattern in export_runner.go. A nil Index with nil Err means "here's a
// progress update, not the final result."
type libraryLoadEvent struct {
	Progress library.Progress
	Final    bool
	Index    *library.Index
	Err      error
}

func runLibraryLoad(ctx context.Context, cfg *config.Config, ch chan<- libraryLoadEvent) {
	defer close(ch)

	idx, err := library.Load(ctx, library.LoadConfig{
		NavidromeDBPath: cfg.NavidromeDBPath,
		MusicPath:       cfg.NavidromeMusicPath,
	}, func(p library.Progress) {
		select {
		case ch <- libraryLoadEvent{Progress: p}:
		case <-ctx.Done():
		}
	})

	select {
	case ch <- libraryLoadEvent{Final: true, Index: idx, Err: err}:
	case <-ctx.Done():
	}
}

func waitForLibraryEvent(ch <-chan libraryLoadEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			// Channel closed without a final event (e.g. cancellation raced
			// the goroutine's last send) — report it as an error rather
			// than silently looking like an empty-but-successful load.
			return libraryLoadEvent{Final: true, Err: context.Canceled}
		}
		return ev
	}
}
