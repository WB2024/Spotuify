package tui

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"spotuify/internal/config"
)

type settingsRow int

const (
	rowClientID settingsRow = iota
	rowClientSecret
	rowExportDir
	rowRedirectPort
	rowDownloadCovers
	rowLogout
	rowSave
	rowBack
	rowCount
)

// settingsAction tells the root model what, if anything, it needs to do in
// response to a settings interaction it can't handle by itself.
type settingsAction int

const (
	settingsNone settingsAction = iota
	settingsBack
	// settingsClientInvalidated means credentials changed (or the user
	// logged out): any cached, already-authenticated Spotify client is no
	// longer valid and must be discarded.
	settingsClientInvalidated
)

// SettingsModel is the Settings screen: Spotify credentials, export
// location, and a couple of export behavior toggles, all editable and
// persisted back to the .env file Load originally read.
type SettingsModel struct {
	cfg *config.Config

	inputs         [4]textinput.Model // indexed by rowClientID..rowRedirectPort
	downloadCovers bool

	cursor  settingsRow
	editing bool

	status    string
	statusErr bool

	width int
}

func newSettings(cfg *config.Config) SettingsModel {
	mk := func(placeholder, val string, password bool) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.SetValue(val)
		ti.CharLimit = 512
		ti.Width = 50
		ti.PromptStyle = accentStyle
		ti.Cursor.Style = accentStyle
		if password {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		return ti
	}

	var inputs [4]textinput.Model
	inputs[rowClientID] = mk("Spotify Client ID", cfg.ClientID, false)
	inputs[rowClientSecret] = mk("Spotify Client Secret", cfg.ClientSecret, true)
	inputs[rowExportDir] = mk("exports", cfg.ExportDir, false)
	inputs[rowRedirectPort] = mk("8080", strconv.Itoa(cfg.RedirectPort), false)

	return SettingsModel{
		cfg:            cfg,
		inputs:         inputs,
		downloadCovers: cfg.DownloadCovers,
	}
}

// Keys returns the keymap currently in effect, for the shared help bar.
func (s SettingsModel) Keys() help.KeyMap {
	if s.editing {
		return settingsEditKeys
	}
	return settingsNavKeys
}

// IsEditing reports whether a text field currently has focus, so the root
// model knows to route every keystroke (including ones that are normally
// global shortcuts, like "?") into the field instead.
func (s SettingsModel) IsEditing() bool { return s.editing }

// SetSize propagates a terminal resize so the credential/options panels
// stay a consistent width instead of each auto-sizing to its own longest
// line.
func (s *SettingsModel) SetSize(width int) {
	s.width = width
}

const settingsPanelMaxWidth = 78

func (s SettingsModel) panelWidth() int {
	w := s.width - 4 // account for the panel's own border + padding
	if w > settingsPanelMaxWidth {
		w = settingsPanelMaxWidth
	}
	if w < 30 {
		w = 30
	}
	return w
}

// Update handles a message for the Settings screen. Key messages are
// dispatched by handleKey; anything else (e.g. the text cursor's blink
// tick) is forwarded to the focused field, if any, so its cursor keeps
// blinking while editing.
func (s SettingsModel) Update(msg tea.Msg) (SettingsModel, tea.Cmd, settingsAction) {
	if km, ok := msg.(tea.KeyMsg); ok {
		return s.handleKey(km)
	}
	if s.editing {
		var cmd tea.Cmd
		s.inputs[s.cursor], cmd = s.inputs[s.cursor].Update(msg)
		return s, cmd, settingsNone
	}
	return s, nil, settingsNone
}

func (s SettingsModel) handleKey(msg tea.KeyMsg) (SettingsModel, tea.Cmd, settingsAction) {
	if s.editing {
		if key.Matches(msg, settingsEditKeys.Confirm) {
			s.inputs[s.cursor].Blur()
			s.editing = false
			return s, nil, settingsNone
		}
		var cmd tea.Cmd
		s.inputs[s.cursor], cmd = s.inputs[s.cursor].Update(msg)
		return s, cmd, settingsNone
	}

	switch {
	case key.Matches(msg, settingsNavKeys.Up):
		if s.cursor > 0 {
			s.cursor--
		}
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Down):
		if s.cursor < rowCount-1 {
			s.cursor++
		}
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Toggle):
		if s.cursor == rowDownloadCovers {
			s.downloadCovers = !s.downloadCovers
		}
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Edit):
		switch s.cursor {
		case rowClientID, rowClientSecret, rowExportDir, rowRedirectPort:
			s.editing = true
			s.status = ""
			cmd := s.inputs[s.cursor].Focus()
			return s, cmd, settingsNone

		case rowDownloadCovers:
			s.downloadCovers = !s.downloadCovers
			return s, nil, settingsNone

		case rowLogout:
			s.doLogout()
			return s, nil, settingsClientInvalidated

		case rowSave:
			if s.doSave() {
				return s, nil, settingsClientInvalidated
			}
			return s, nil, settingsNone

		case rowBack:
			return s, nil, settingsBack
		}

	case key.Matches(msg, settingsNavKeys.Back):
		return s, nil, settingsBack
	}

	return s, nil, settingsNone
}

// doSave validates and applies the form's values to cfg and persists them.
// It reports whether Spotify credentials changed, which means any cached
// authenticated client is now stale.
func (s *SettingsModel) doSave() bool {
	port, err := strconv.Atoi(strings.TrimSpace(s.inputs[rowRedirectPort].Value()))
	if err != nil || port <= 0 || port > 65535 {
		s.status = "Redirect port must be a number between 1 and 65535."
		s.statusErr = true
		return false
	}

	exportDir := strings.TrimSpace(s.inputs[rowExportDir].Value())
	if exportDir == "" {
		exportDir = "exports"
	}

	newClientID := strings.TrimSpace(s.inputs[rowClientID].Value())
	newClientSecret := strings.TrimSpace(s.inputs[rowClientSecret].Value())
	credsChanged := newClientID != s.cfg.ClientID || newClientSecret != s.cfg.ClientSecret

	s.cfg.ClientID = newClientID
	s.cfg.ClientSecret = newClientSecret
	s.cfg.ExportDir = exportDir
	s.cfg.RedirectPort = port
	s.cfg.DownloadCovers = s.downloadCovers

	if err := s.cfg.Save(); err != nil {
		s.status = "Save failed: " + err.Error()
		s.statusErr = true
		return false
	}

	if credsChanged {
		_ = os.Remove(s.cfg.TokenCachePath) // old refresh token belongs to the old app credentials
	}

	s.status = "Saved to " + s.cfg.EnvPath
	s.statusErr = false
	return credsChanged
}

func (s *SettingsModel) doLogout() {
	_ = os.Remove(s.cfg.TokenCachePath)
	s.status = "Logged out. You'll be asked to log in again next time you export."
	s.statusErr = false
}

func (s SettingsModel) View() string {
	var b strings.Builder
	panelW := s.panelWidth()

	group := func(title string, rows func(*strings.Builder)) {
		var body strings.Builder
		rows(&body)
		panel := panelStyle.Width(panelW)
		b.WriteString(panelTitleStyle.Render(title) + "\n")
		b.WriteString(panel.Render(strings.TrimRight(body.String(), "\n")) + "\n\n")
	}

	textRow := func(b *strings.Builder, idx settingsRow, label, hint string) {
		selected := s.cursor == idx
		marker := "  "
		labelStyle := dimStyle
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
		}
		b.WriteString(labelStyle.Render(marker+label) + "\n")
		fieldStyle := lipgloss.NewStyle()
		if selected && s.editing {
			fieldStyle = fieldStyle.Foreground(spotifyGreen)
		}
		b.WriteString("    " + fieldStyle.Render(s.inputs[idx].View()) + "\n")
		if hint != "" {
			b.WriteString(fadedStyle.Width(panelW-4).Render("    "+hint) + "\n")
		}
	}

	group("Spotify Credentials", func(body *strings.Builder) {
		textRow(body, rowClientID, "Client ID", "")
		textRow(body, rowClientSecret, "Client Secret", "")
	})

	group("Export Options", func(body *strings.Builder) {
		textRow(body, rowExportDir, "Export directory", "Where playlist.json / tracks.csv / cover art are written")
		textRow(body, rowRedirectPort, "OAuth redirect port", "Must match a Redirect URI in your Spotify Dashboard app: http://127.0.0.1:<port>/callback")

		selected := s.cursor == rowDownloadCovers
		marker := "  "
		labelStyle := dimStyle
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
		}
		box := "☐"
		if s.downloadCovers {
			box = "☑"
		}
		body.WriteString(labelStyle.Render(marker+box+" Download cover art during export") + "\n")
	})

	actionRow := func(idx settingsRow, label string, style lipgloss.Style) string {
		selected := s.cursor == idx
		marker := "  "
		labelStyle := style
		if selected {
			marker = "▸ "
			labelStyle = style.Bold(true).Underline(true)
		}
		return labelStyle.Render(marker + label)
	}

	actions := lipgloss.JoinHorizontal(lipgloss.Top,
		actionRow(rowLogout, "Log out", warnStyle),
		"    ",
		actionRow(rowSave, "Save", successStyle),
		"    ",
		actionRow(rowBack, "Back to menu", dimStyle),
	)
	b.WriteString(actions + "\n")

	if s.status != "" {
		st := successStyle
		if s.statusErr {
			st = errorStyle
		}
		b.WriteString("\n" + st.Render(s.status) + "\n")
	}

	return b.String()
}
