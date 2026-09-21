package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
)

// Run starts the Spotuify TUI and blocks until the user quits.
func Run(ctx context.Context, cancel context.CancelFunc, cfg *config.Config) error {
	m := New(ctx, cancel, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
