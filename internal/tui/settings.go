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
	rowNavidromeDB
	rowNavidromeMusicPath
	rowM3U8Dir
	rowNavidromeAPIURL
	rowNavidromeAPIUsername
	rowNavidromeAPIPassword
	numTextRows // marks the end of the text-input rows (see inputs array)

	rowDownloadCovers
	rowResolveMBID
	rowFuzzyMatch

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

	inputs         [numTextRows]textinput.Model // indexed by rowClientID..rowM3U8Dir
	downloadCovers bool
	resolveMBID    bool
	fuzzyMatch     bool

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

	var inputs [numTextRows]textinput.Model
	inputs[rowClientID] = mk("Spotify Client ID", cfg.ClientID, false)
	inputs[rowClientSecret] = mk("Spotify Client Secret", cfg.ClientSecret, true)
	inputs[rowExportDir] = mk("exports", cfg.ExportDir, false)
	inputs[rowRedirectPort] = mk("8080", strconv.Itoa(cfg.RedirectPort), false)
	inputs[rowNavidromeDB] = mk("/path/to/navidrome/data/navidrome.db", cfg.NavidromeDBPath, false)
	inputs[rowNavidromeMusicPath] = mk("/path/to/your/music", cfg.NavidromeMusicPath, false)
	inputs[rowM3U8Dir] = mk("playlists", cfg.M3U8Dir, false)
	inputs[rowNavidromeAPIURL] = mk("http://navidrome.example.com", cfg.NavidromeAPIURL, false)
	inputs[rowNavidromeAPIUsername] = mk("Navidrome username", cfg.NavidromeUsername, false)
	inputs[rowNavidromeAPIPassword] = mk("(not set — cover art upload skipped)", cfg.NavidromePassword, true)

	return SettingsModel{
		cfg:            cfg,
		inputs:         inputs,
		downloadCovers: cfg.DownloadCovers,
		resolveMBID:    cfg.ResolveMusicBrainzISRC,
		fuzzyMatch:     cfg.EnableFuzzyMatching,
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
		s.toggle(s.cursor)
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Edit):
		switch {
		case s.cursor < numTextRows:
			s.editing = true
			s.status = ""
			cmd := s.inputs[s.cursor].Focus()
			return s, cmd, settingsNone

		case s.cursor == rowDownloadCovers, s.cursor == rowResolveMBID, s.cursor == rowFuzzyMatch:
			s.toggle(s.cursor)
			return s, nil, settingsNone

		case s.cursor == rowLogout:
			s.doLogout()
			return s, nil, settingsClientInvalidated

		case s.cursor == rowSave:
			if s.doSave() {
				return s, nil, settingsClientInvalidated
			}
			return s, nil, settingsNone

		case s.cursor == rowBack:
			return s, nil, settingsBack
		}

	case key.Matches(msg, settingsNavKeys.Back):
		return s, nil, settingsBack
	}

	return s, nil, settingsNone
}

func (s *SettingsModel) toggle(row settingsRow) {
	switch row {
	case rowDownloadCovers:
		s.downloadCovers = !s.downloadCovers
	case rowResolveMBID:
		s.resolveMBID = !s.resolveMBID
	case rowFuzzyMatch:
		s.fuzzyMatch = !s.fuzzyMatch
	}
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
	m3u8Dir := strings.TrimSpace(s.inputs[rowM3U8Dir].Value())
	if m3u8Dir == "" {
		m3u8Dir = "playlists"
	}

	newClientID := strings.TrimSpace(s.inputs[rowClientID].Value())
	newClientSecret := strings.TrimSpace(s.inputs[rowClientSecret].Value())
	credsChanged := newClientID != s.cfg.ClientID || newClientSecret != s.cfg.ClientSecret

	s.cfg.ClientID = newClientID
	s.cfg.ClientSecret = newClientSecret
	s.cfg.ExportDir = exportDir
	s.cfg.RedirectPort = port
	s.cfg.DownloadCovers = s.downloadCovers
	s.cfg.NavidromeDBPath = strings.TrimSpace(s.inputs[rowNavidromeDB].Value())
	s.cfg.NavidromeMusicPath = strings.TrimSpace(s.inputs[rowNavidromeMusicPath].Value())
	s.cfg.M3U8Dir = m3u8Dir
	s.cfg.ResolveMusicBrainzISRC = s.resolveMBID
	s.cfg.EnableFuzzyMatching = s.fuzzyMatch
	s.cfg.NavidromeAPIURL = strings.TrimRight(strings.TrimSpace(s.inputs[rowNavidromeAPIURL].Value()), "/")
	s.cfg.NavidromeUsername = strings.TrimSpace(s.inputs[rowNavidromeAPIUsername].Value())
	s.cfg.NavidromePassword = strings.TrimSpace(s.inputs[rowNavidromeAPIPassword].Value())

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

	toggleRow := func(body *strings.Builder, idx settingsRow, label string, on bool) {
		selected := s.cursor == idx
		marker := "  "
		labelStyle := dimStyle
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
		}
		box := "☐"
		if on {
			box = "☑"
		}
		body.WriteString(labelStyle.Render(marker+box+" "+label) + "\n")
	}

	group("Spotify Credentials", func(body *strings.Builder) {
		textRow(body, rowClientID, "Client ID", "")
		textRow(body, rowClientSecret, "Client Secret", "")
	})

	group("Export Options", func(body *strings.Builder) {
		textRow(body, rowExportDir, "Export directory", "Where playlist.json / tracks.csv / cover art are written")
		textRow(body, rowRedirectPort, "OAuth redirect port", "Must match a Redirect URI in your Spotify Dashboard app: http://127.0.0.1:<port>/callback")
		toggleRow(body, rowDownloadCovers, "Download cover art during export", s.downloadCovers)
	})

	group("Local Library Matching", func(body *strings.Builder) {
		textRow(body, rowNavidromeDB, "Navidrome database path", "The navidrome.db file on this machine")
		textRow(body, rowNavidromeMusicPath, "Navidrome music path", "This machine's path to Navidrome's music root (its ND_MUSICFOLDER)")
		textRow(body, rowM3U8Dir, "Playlist output directory", "Where .m3u8 files, missing-track reports, and cover art are written")
		toggleRow(body, rowResolveMBID, "Bridge via MusicBrainz for tracks with no ISRC tag (rate-limited, cached)", s.resolveMBID)
		toggleRow(body, rowFuzzyMatch, "Fuzzy-match tracks with no ISRC/MusicBrainz data", s.fuzzyMatch)
	})

	group("Navidrome Cover Art Upload (optional)", func(body *strings.Builder) {
		textRow(body, rowNavidromeAPIURL, "Navidrome server URL", "e.g. http://navidrome.example.com — leave blank to skip cover art upload")
		textRow(body, rowNavidromeAPIUsername, "Navidrome username", "")
		textRow(body, rowNavidromeAPIPassword, "Navidrome password", "Used only to log in via POST /auth/login for a JWT; never sent anywhere else")
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
