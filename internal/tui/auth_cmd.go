package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/auth"
	"spotuify/internal/config"
)

// authenticate runs (possibly interactive, browser-based) Spotify login and
// reports the resulting authenticated http.Client back to the Update loop.
func authenticate(ctx context.Context, cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		hc, err := auth.GetClient(ctx, cfg)
		if err != nil {
			return authDoneMsg{err: err}
		}
		return authDoneMsg{httpClient: hc}
	}
}
