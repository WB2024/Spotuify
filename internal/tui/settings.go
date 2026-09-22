package tui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"spotuify/internal/config"
	"spotuify/internal/lidarrapi"
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
	rowNavidromeGroup
	rowNavidromeAPIURL
	rowNavidromeAPIUsername
	rowNavidromeAPIPassword
	rowLidarrURL
	rowLidarrAPIKey
	numTextRows // marks the end of the text-input rows (see inputs array)

	rowDownloadCovers
	rowResolveMBID
	rowFuzzyMatch

	// Lidarr option rows: their choices come from Lidarr itself (see
	// lidarrOptions), so they cycle rather than take typed input.
	rowLidarrRootFolder
	rowLidarrQualityProfile
	rowLidarrMetadataProfile
	rowLidarrAddSearch

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

	inputs         [numTextRows]textinput.Model // indexed by rowClientID..rowLidarrAPIKey
	downloadCovers bool
	resolveMBID    bool
	fuzzyMatch     bool

	// Lidarr choices. The option lists are fetched from Lidarr on first use
	// of one of its option rows (needs URL + API key filled in above them).
	lidarrRootFolder string
	lidarrQualityID  int
	lidarrMetadataID int
	lidarrAddSearch  bool
	lidarrOptions    *lidarrOptions
	lidarrLoading    bool

	cursor  settingsRow
	editing bool

	status    string
	statusErr bool

	width, height int
	viewport      viewport.Model
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
	inputs[rowNavidromeGroup] = mk("Spotify/SR/", cfg.NavidromeGroup, false)
	inputs[rowNavidromeAPIURL] = mk("http://navidrome.example.com", cfg.NavidromeAPIURL, false)
	inputs[rowNavidromeAPIUsername] = mk("Navidrome username", cfg.NavidromeUsername, false)
	inputs[rowNavidromeAPIPassword] = mk("(not set — cover art upload skipped)", cfg.NavidromePassword, true)
	inputs[rowLidarrURL] = mk("http://lidarr.example.com:8686 — leave blank to disable", cfg.LidarrURL, false)
	inputs[rowLidarrAPIKey] = mk("Lidarr API key (Settings › General in Lidarr)", cfg.LidarrAPIKey, true)

	vp := viewport.New(0, 0)

	return SettingsModel{
		cfg:              cfg,
		inputs:           inputs,
		downloadCovers:   cfg.DownloadCovers,
		resolveMBID:      cfg.ResolveMusicBrainzISRC,
		fuzzyMatch:       cfg.EnableFuzzyMatching,
		lidarrRootFolder: cfg.LidarrRootFolder,
		lidarrQualityID:  cfg.LidarrQualityProfileID,
		lidarrMetadataID: cfg.LidarrMetadataProfileID,
		lidarrAddSearch:  cfg.LidarrAddAndSearch,
		viewport:         vp,
	}
}

// lidarrOptions is what Lidarr reports as available to pick from.
type lidarrOptions struct {
	rootFolders []lidarrapi.RootFolder
	quality     []lidarrapi.Profile
	metadata    []lidarrapi.Profile
}

type lidarrOptionsMsg struct {
	opts *lidarrOptions
	err  error
}

func fetchLidarrOptions(url, apiKey string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		c := lidarrapi.New(url, apiKey)
		roots, err := c.RootFolders(ctx)
		if err != nil {
			return lidarrOptionsMsg{err: err}
		}
		quality, err := c.QualityProfiles(ctx)
		if err != nil {
			return lidarrOptionsMsg{err: err}
		}
		metadata, err := c.MetadataProfiles(ctx)
		if err != nil {
			return lidarrOptionsMsg{err: err}
		}
		return lidarrOptionsMsg{opts: &lidarrOptions{rootFolders: roots, quality: quality, metadata: metadata}}
	}
}

func (s SettingsModel) isLidarrOptionRow(row settingsRow) bool {
	return row == rowLidarrRootFolder || row == rowLidarrQualityProfile || row == rowLidarrMetadataProfile
}

// cycleLidarrOption advances the given option row to Lidarr's next choice,
// or — if the choices haven't been fetched yet — fetches them first, using
// whatever URL/API key are typed in above.
func (s *SettingsModel) cycleLidarrOption(row settingsRow) tea.Cmd {
	if s.lidarrOptions == nil {
		if s.lidarrLoading {
			return nil
		}
		url := strings.TrimRight(strings.TrimSpace(s.inputs[rowLidarrURL].Value()), "/")
		apiKey := strings.TrimSpace(s.inputs[rowLidarrAPIKey].Value())
		if url == "" || apiKey == "" {
			s.status = "Fill in the Lidarr URL and API key first."
			s.statusErr = true
			return nil
		}
		s.lidarrLoading = true
		s.status = "Loading root folders and profiles from Lidarr..."
		s.statusErr = false
		return fetchLidarrOptions(url, apiKey)
	}

	o := s.lidarrOptions
	switch row {
	case rowLidarrRootFolder:
		if len(o.rootFolders) == 0 {
			return nil
		}
		next := o.rootFolders[0]
		for i, rf := range o.rootFolders {
			if rf.Path == s.lidarrRootFolder {
				next = o.rootFolders[(i+1)%len(o.rootFolders)]
				break
			}
		}
		s.lidarrRootFolder = next.Path
		// Lidarr root folders carry their own default profiles — adopt
		// them when nothing's been picked yet, which is what Lidarr's own
		// UI does too.
		if s.lidarrQualityID == 0 {
			s.lidarrQualityID = next.DefaultQualityProfileID
		}
		if s.lidarrMetadataID == 0 {
			s.lidarrMetadataID = next.DefaultMetadataProfileID
		}
	case rowLidarrQualityProfile:
		s.lidarrQualityID = nextProfileID(o.quality, s.lidarrQualityID)
	case rowLidarrMetadataProfile:
		s.lidarrMetadataID = nextProfileID(o.metadata, s.lidarrMetadataID)
	}
	return nil
}

func nextProfileID(profiles []lidarrapi.Profile, current int) int {
	if len(profiles) == 0 {
		return current
	}
	for i, p := range profiles {
		if p.ID == current {
			return profiles[(i+1)%len(profiles)].ID
		}
	}
	return profiles[0].ID
}

func profileName(profiles []lidarrapi.Profile, id int) string {
	for _, p := range profiles {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// lidarrOptionLabel is what an option row shows as its current value.
func (s SettingsModel) lidarrOptionLabel(row settingsRow) string {
	unloaded := func(current string) string {
		if current == "" {
			return fadedStyle.Render("(not set — enter to load choices from Lidarr)")
		}
		return bodyStyle.Render(current) + fadedStyle.Render("  (enter to load choices from Lidarr)")
	}
	o := s.lidarrOptions
	switch row {
	case rowLidarrRootFolder:
		if o == nil {
			return unloaded(s.lidarrRootFolder)
		}
		for _, rf := range o.rootFolders {
			if rf.Path == s.lidarrRootFolder {
				return bodyStyle.Render(rf.Path) + dimStyle.Render("  ("+rf.Name+")")
			}
		}
		return fadedStyle.Render("(none picked — enter to cycle)")
	case rowLidarrQualityProfile:
		if o == nil {
			return unloaded(idLabel(s.lidarrQualityID))
		}
		if n := profileName(o.quality, s.lidarrQualityID); n != "" {
			return bodyStyle.Render(n)
		}
		return fadedStyle.Render("(none picked — enter to cycle)")
	case rowLidarrMetadataProfile:
		if o == nil {
			return unloaded(idLabel(s.lidarrMetadataID))
		}
		if n := profileName(o.metadata, s.lidarrMetadataID); n != "" {
			return bodyStyle.Render(n)
		}
		return fadedStyle.Render("(none picked — enter to cycle)")
	}
	return ""
}

func idLabel(id int) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("profile #%d", id)
}

// Keys returns the keymap currently in effect, for the shared help bar.
func (s SettingsModel) Keys() help.KeyMap {
	if s.editing {
		return settingsFieldEditKeys
	}
	return settingsNavKeys
}

// IsEditing reports whether a text field currently has focus, so the root
// model knows to route every keystroke (including ones that are normally
// global shortcuts, like "?") into the field instead.
func (s SettingsModel) IsEditing() bool { return s.editing }

// SetSize propagates a terminal resize so the credential/options panels
// stay a consistent width instead of each auto-sizing to its own longest
// line, and so the form's scrollable viewport (the whole form is taller
// than one screenful — see View) knows how much space it actually has.
func (s *SettingsModel) SetSize(width, height int) {
	s.width = width
	s.height = height
	s.viewport.Width = width
	// Reserve 2 lines for the status footer (see View) so a save/logout
	// confirmation - triggered by ctrl+s from anywhere, not just the Save
	// row - is always visible instead of landing wherever the scrollable
	// content happens to be, off-screen more often than not.
	s.viewport.Height = clampInt(height-2, 3, height)
	s.followCursor() // a shrinking terminal can push the current row out of view
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
	if om, ok := msg.(lidarrOptionsMsg); ok {
		s.lidarrLoading = false
		if om.err != nil {
			s.status = "Lidarr: " + om.err.Error()
			s.statusErr = true
			return s, nil, settingsNone
		}
		s.lidarrOptions = om.opts
		s.status = fmt.Sprintf("Loaded %d root folder(s), %d quality and %d metadata profile(s) from Lidarr — enter/space cycles each.",
			len(om.opts.rootFolders), len(om.opts.quality), len(om.opts.metadata))
		s.statusErr = false
		return s, nil, settingsNone
	}
	if s.editing {
		var cmd tea.Cmd
		s.inputs[s.cursor], cmd = s.inputs[s.cursor].Update(msg)
		return s, cmd, settingsNone
	}
	// Mouse wheel scrolling (viewport handles tea.MouseMsg natively,
	// unlike table/list elsewhere in this app) and anything else that
	// isn't cursor navigation, which handleKey already fully owns.
	//
	// SetContent must run here (a pointer-friendly path whose result is
	// returned and persisted), not in View, which runs on a value receiver
	// - any state View mutates is a throwaway copy, so if SetContent only
	// ever ran there the viewport's line buffer would stay permanently
	// empty and every scroll would clamp straight back to 0.
	content, _ := s.renderContent()
	s.viewport.SetContent(content)
	var cmd tea.Cmd
	s.viewport, cmd = s.viewport.Update(msg)
	return s, cmd, settingsNone
}

func (s SettingsModel) handleKey(msg tea.KeyMsg) (SettingsModel, tea.Cmd, settingsAction) {
	if s.editing {
		if key.Matches(msg, settingsFieldEditKeys.Save) {
			s.inputs[s.cursor].Blur()
			s.editing = false
			if s.doSave() {
				return s, nil, settingsClientInvalidated
			}
			return s, nil, settingsNone
		}
		if key.Matches(msg, settingsEditKeys.Confirm) {
			s.inputs[s.cursor].Blur()
			s.editing = false
			return s, nil, settingsNone
		}
		var cmd tea.Cmd
		s.inputs[s.cursor], cmd = s.inputs[s.cursor].Update(msg)
		return s, cmd, settingsNone
	}

	if key.Matches(msg, settingsNavKeys.Save) {
		if s.doSave() {
			return s, nil, settingsClientInvalidated
		}
		return s, nil, settingsNone
	}

	switch {
	case key.Matches(msg, settingsNavKeys.Up):
		if s.cursor > 0 {
			s.cursor--
			if s.cursor == numTextRows { // marker value, not a row
				s.cursor--
			}
			s.followCursor()
		}
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Down):
		if s.cursor < rowCount-1 {
			s.cursor++
			if s.cursor == numTextRows {
				s.cursor++
			}
			s.followCursor()
		}
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Toggle):
		if s.isLidarrOptionRow(s.cursor) {
			return s, s.cycleLidarrOption(s.cursor), settingsNone
		}
		s.toggle(s.cursor)
		return s, nil, settingsNone

	case key.Matches(msg, settingsNavKeys.Edit):
		switch {
		case s.cursor < numTextRows:
			s.editing = true
			s.status = ""
			cmd := s.inputs[s.cursor].Focus()
			return s, cmd, settingsNone

		case s.cursor == rowDownloadCovers, s.cursor == rowResolveMBID, s.cursor == rowFuzzyMatch, s.cursor == rowLidarrAddSearch:
			s.toggle(s.cursor)
			return s, nil, settingsNone

		case s.isLidarrOptionRow(s.cursor):
			return s, s.cycleLidarrOption(s.cursor), settingsNone

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
	case rowLidarrAddSearch:
		s.lidarrAddSearch = !s.lidarrAddSearch
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
	s.cfg.NavidromeGroup = strings.TrimSpace(s.inputs[rowNavidromeGroup].Value())
	s.cfg.ResolveMusicBrainzISRC = s.resolveMBID
	s.cfg.EnableFuzzyMatching = s.fuzzyMatch
	s.cfg.NavidromeAPIURL = strings.TrimRight(strings.TrimSpace(s.inputs[rowNavidromeAPIURL].Value()), "/")
	s.cfg.NavidromeUsername = strings.TrimSpace(s.inputs[rowNavidromeAPIUsername].Value())
	s.cfg.NavidromePassword = strings.TrimSpace(s.inputs[rowNavidromeAPIPassword].Value())
	s.cfg.LidarrURL = strings.TrimRight(strings.TrimSpace(s.inputs[rowLidarrURL].Value()), "/")
	s.cfg.LidarrAPIKey = strings.TrimSpace(s.inputs[rowLidarrAPIKey].Value())
	s.cfg.LidarrRootFolder = s.lidarrRootFolder
	s.cfg.LidarrQualityProfileID = s.lidarrQualityID
	s.cfg.LidarrMetadataProfileID = s.lidarrMetadataID
	s.cfg.LidarrAddAndSearch = s.lidarrAddSearch

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

// renderContent builds the form's full text content (every group, field,
// hint, and the action row) and reports which line the currently-selected
// row landed on — see settingsCursorMarker and the View/followCursor split
// below for why the two are bundled into one method rather than measured
// separately.
func (s SettingsModel) renderContent() (string, int) {
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
		mark := ""
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
			mark = settingsCursorMarker
		}
		b.WriteString(mark + labelStyle.Render(marker+label) + "\n")
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
		mark := ""
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
			mark = settingsCursorMarker
		}
		box := "☐"
		if on {
			box = "☑"
		}
		body.WriteString(mark + labelStyle.Render(marker+box+" "+label) + "\n")
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
		textRow(body, rowNavidromeGroup, "Navidrome group", "Prefix before the playlist name in the #PLAYLIST directive — \"/\" nests as folders in Navidrome/Feishin's sidebar. Overridable per playlist from the Match screen's playlist list (press g).")
		toggleRow(body, rowResolveMBID, "Bridge via MusicBrainz for tracks with no ISRC tag (rate-limited, cached)", s.resolveMBID)
		toggleRow(body, rowFuzzyMatch, "Fuzzy-match tracks with no ISRC/MusicBrainz data", s.fuzzyMatch)
	})

	group("Navidrome Cover Art Upload (optional)", func(body *strings.Builder) {
		textRow(body, rowNavidromeAPIURL, "Navidrome server URL", "e.g. http://navidrome.example.com — leave blank to skip cover art upload")
		textRow(body, rowNavidromeAPIUsername, "Navidrome username", "")
		textRow(body, rowNavidromeAPIPassword, "Navidrome password", "Used only to log in via POST /auth/login for a JWT; never sent anywhere else")
	})

	optionRow := func(body *strings.Builder, idx settingsRow, label, hint string) {
		selected := s.cursor == idx
		marker := "  "
		labelStyle := dimStyle
		mark := ""
		if selected {
			marker = "▸ "
			labelStyle = selectedRowStyle
			mark = settingsCursorMarker
		}
		body.WriteString(mark + labelStyle.Render(marker+label) + "\n")
		body.WriteString("    " + s.lidarrOptionLabel(idx) + "\n")
		if hint != "" {
			body.WriteString(fadedStyle.Width(panelW-4).Render("    "+hint) + "\n")
		}
	}

	group("Lidarr (optional)", func(body *strings.Builder) {
		textRow(body, rowLidarrURL, "Lidarr server URL", "Lets you add a missing track's album to Lidarr from the match results (l / L)")
		textRow(body, rowLidarrAPIKey, "Lidarr API key", "")
		optionRow(body, rowLidarrRootFolder, "Root folder", "Where Lidarr puts artists it doesn't have yet; picking one also adopts its default profiles")
		optionRow(body, rowLidarrQualityProfile, "Quality profile", "")
		optionRow(body, rowLidarrMetadataProfile, "Metadata profile", "")
		toggleRow(body, rowLidarrAddSearch, "Add + search by default (off = add only, Lidarr searches on its own schedule) — overridable per add with s", s.lidarrAddSearch)
	})

	actionRow := func(idx settingsRow, label string, style lipgloss.Style) string {
		selected := s.cursor == idx
		marker := "  "
		labelStyle := style
		mark := ""
		if selected {
			marker = "▸ "
			labelStyle = style.Bold(true).Underline(true)
			mark = settingsCursorMarker
		}
		return mark + labelStyle.Render(marker+label)
	}

	actions := lipgloss.JoinHorizontal(lipgloss.Top,
		actionRow(rowLogout, "Log out", warnStyle),
		"    ",
		actionRow(rowSave, "Save", successStyle),
		"    ",
		actionRow(rowBack, "Back to menu", dimStyle),
	)
	b.WriteString(actions + "\n")

	content := b.String()
	selectedLine := 0
	if idx := strings.IndexByte(content, 0); idx >= 0 {
		selectedLine = strings.Count(content[:idx], "\n")
		content = content[:idx] + content[idx+1:]
	}
	return content, selectedLine
}

// settingsCursorMarker is a sentinel prefixed onto whichever row's label is
// currently selected, purely so renderContent can find out which line that
// row landed on in the *final*, fully-rendered output (after every group's
// border/padding and any hint-text wrapping has already happened) without
// having to predict any of that ahead of time — it just searches the real
// output. Stripped before renderContent returns. A raw NUL byte never
// appears in this form's own content otherwise, so there's no ambiguity to
// worry about.
const settingsCursorMarker = "\x00"

// View renders the form through the viewport — this is what makes it
// scrollable at all (previously the whole form, every group/field/hint,
// was dumped as one unbounded string, so a terminal shorter than that
// could only ever show whatever portion the terminal's own scrollback
// happened to land on, with no way back up). It deliberately does *not*
// touch the viewport's scroll position itself — see followCursor for why.
func (s SettingsModel) View() string {
	content, _ := s.renderContent()
	s.viewport.SetContent(content)
	view := s.viewport.View()
	if s.status != "" {
		st := successStyle
		if s.statusErr {
			st = errorStyle
		}
		view += "\n\n" + st.Render(s.status)
	}
	return view
}

// followCursor scrolls just enough to keep the current row in view, with a
// little breathing room, rather than jammed right against the top or
// bottom edge. Called from handleKey whenever s.cursor actually moves —
// deliberately *not* from View, which runs on every single render
// (including a mouse-wheel scroll, which doesn't move the cursor at all):
// snapping back to the cursor's row on every render would fight, and
// immediately undo, any independent wheel scrolling within the same frame.
func (s *SettingsModel) followCursor() {
	content, selectedLine := s.renderContent()
	// Persist the content into the real model's viewport (a pointer
	// receiver, unlike View's) so maxYOffset reflects the actual line
	// count - otherwise SetYOffset below always clamps back to 0.
	s.viewport.SetContent(content)
	const scrollMargin = 2
	switch {
	case selectedLine < s.viewport.YOffset+scrollMargin:
		s.viewport.SetYOffset(selectedLine - scrollMargin)
	case selectedLine > s.viewport.YOffset+s.viewport.Height-1-scrollMargin:
		s.viewport.SetYOffset(selectedLine - s.viewport.Height + 1 + scrollMargin)
	}
}
