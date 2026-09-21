// Package tui implements Spotuify's Bubble Tea interface: a main menu
// leading to playlist export and settings, all sharing a common visual
// chrome (gradient logo, bordered panels, a contextual help bar) so new
// screens can be added later without re-deriving the look and feel.
package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
)

type screen int

const (
	screenMenu screen = iota
	screenSettings
	screenExport
	screenMatch
)

// Model is the root Bubble Tea model for Spotuify. It owns cross-cutting
// concerns (the whole-run context, the current screen, the shared help
// bar) and delegates everything else to the active screen.
type Model struct {
	// ctx is the whole-run context (cancelled only on OS interrupt/quit).
	ctx    context.Context
	cancel context.CancelFunc

	// opCancel, if non-nil, cancels the context backing the export screen's
	// current operation (auth, playlist load, or export run) without
	// tearing down the whole app — used when the user backs out to the menu.
	opCancel context.CancelFunc

	cfg *config.Config

	screen screen

	menuCursor  menuItem
	menuMessage string

	settings SettingsModel
	export   ExportModel
	match    MatchModel

	help help.Model

	width, height int
}

// New builds the initial Spotuify TUI model. ctx governs the whole run;
// cancel is called on quit to stop any in-flight requests.
func New(ctx context.Context, cancel context.CancelFunc, cfg *config.Config) Model {
	h := help.New()
	h.Styles.ShortKey = h.Styles.ShortKey.Foreground(accent)
	h.Styles.ShortDesc = h.Styles.ShortDesc.Foreground(muted)
	h.Styles.ShortSeparator = h.Styles.ShortSeparator.Foreground(faint)
	h.Styles.FullKey = h.Styles.FullKey.Foreground(accent)
	h.Styles.FullDesc = h.Styles.FullDesc.Foreground(muted)
	h.Styles.FullSeparator = h.Styles.FullSeparator.Foreground(faint)

	return Model{
		ctx:      ctx,
		cancel:   cancel,
		cfg:      cfg,
		screen:   screenMenu,
		settings: newSettings(cfg),
		export:   newExportModel(cfg),
		match:    newMatchModel(cfg),
		help:     h,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.SetWindowTitle(appName)
}

// capturingText reports whether the active screen currently wants every
// keystroke verbatim (typing in a field, filtering a list) — in which case
// global shortcuts like "?" must not be intercepted.
func (m Model) capturingText() bool {
	if m.screen == screenSettings && m.settings.IsEditing() {
		return true
	}
	if m.screen == screenExport && m.export.IsFiltering() {
		return true
	}
	if m.screen == screenMatch && (m.match.IsFiltering() || m.match.IsEditingGroup()) {
		return true
	}
	return false
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h, v := appPad.GetFrameSize()
		innerW := msg.Width - h
		innerH := msg.Height - v - 4 // logo line + blank + help line + blank
		if innerH < 5 {
			innerH = 5
		}
		m.help.Width = innerW
		m.export.SetSize(innerW, innerH)
		m.match.SetSize(innerW, innerH)
		m.settings.SetSize(innerW)
		return m, nil

	case tea.KeyMsg:
		if key.Matches(msg, globalKeys.Quit) {
			if m.opCancel != nil {
				m.opCancel()
			}
			m.cancel()
			return m, tea.Quit
		}
		if key.Matches(msg, globalKeys.Help) && !m.capturingText() {
			m.help.ShowAll = !m.help.ShowAll
			return m, nil
		}
	}

	switch m.screen {
	case screenMenu:
		return m.updateMenu(msg)

	case screenSettings:
		var cmd tea.Cmd
		var action settingsAction
		m.settings, cmd, action = m.settings.Update(msg)
		switch action {
		case settingsBack:
			m.screen = screenMenu
		case settingsClientInvalidated:
			m.export.InvalidateClient()
			m.match.InvalidateClient()
		}
		return m, cmd

	case screenExport:
		var cmd tea.Cmd
		var action exportAction
		m.export, cmd, action = m.export.Update(msg)
		if action == exportActionBack {
			m.backToMenu()
		}
		return m, cmd

	case screenMatch:
		var cmd tea.Cmd
		var action matchAction
		m.match, cmd, action = m.match.Update(msg)
		if action == matchActionBack {
			m.backToMenu()
		}
		return m, cmd
	}

	return m, nil
}

func (m Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch {
	case key.Matches(km, menuKeys.Quit):
		m.cancel()
		return m, tea.Quit

	case key.Matches(km, menuKeys.Up):
		if m.menuCursor > 0 {
			m.menuCursor--
		}

	case key.Matches(km, menuKeys.Down):
		if m.menuCursor < menuItemCount-1 {
			m.menuCursor++
		}

	case key.Matches(km, menuKeys.Select):
		switch m.menuCursor {
		case menuExport:
			return m.startExportScreen()
		case menuMatch:
			return m.startMatchScreen()
		case menuSettings:
			m.screen = screenSettings
			m.menuMessage = ""
		}
	}

	return m, nil
}

func (m Model) startExportScreen() (tea.Model, tea.Cmd) {
	if err := m.cfg.Validate(); err != nil {
		m.menuMessage = err.Error()
		return m, nil
	}
	m.menuMessage = ""
	m.screen = screenExport

	var cmd tea.Cmd
	m.export, cmd = m.export.Enter(m.opCtx())
	return m, cmd
}

func (m Model) startMatchScreen() (tea.Model, tea.Cmd) {
	if err := m.cfg.Validate(); err != nil {
		m.menuMessage = err.Error()
		return m, nil
	}
	if err := m.cfg.ValidateLibrary(); err != nil {
		m.menuMessage = err.Error()
		return m, nil
	}
	m.menuMessage = ""
	m.screen = screenMatch

	var cmd tea.Cmd
	m.match, cmd = m.match.Enter(m.opCtx())
	return m, cmd
}

// opCtx returns the context for the current export-screen operation,
// creating it (as a cancelable child of the whole-run context) if needed.
func (m *Model) opCtx() context.Context {
	ctx, cancel := context.WithCancel(m.ctx)
	m.opCancel = cancel
	return ctx
}

// backToMenu tears down any in-flight export-screen operation and returns
// to the main menu.
func (m *Model) backToMenu() {
	if m.opCancel != nil {
		m.opCancel()
		m.opCancel = nil
	}
	m.screen = screenMenu
}

func (m Model) View() string {
	switch m.screen {
	case screenMenu:
		return frame(m.width, m.height, "", renderMainMenu(m.menuCursor, m.menuMessage), renderHelp(m.help, menuKeys))

	case screenSettings:
		return frame(m.width, m.height, "Settings", m.settings.View(), renderHelp(m.help, m.settings.Keys()))

	case screenExport:
		return frame(m.width, m.height, "Export Playlists", m.export.View(), renderHelp(m.help, m.export.Keys()))

	case screenMatch:
		return frame(m.width, m.height, "Match to Local Library", m.match.View(), renderHelp(m.help, m.match.Keys()))
	}
	return ""
}
