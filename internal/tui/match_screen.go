package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"spotuify/internal/config"
	"spotuify/internal/library"
	"spotuify/internal/m3u8"
	"spotuify/internal/match"
	"spotuify/internal/spotifyapi"
)

type matchScreenState int

const (
	matchStateAuthenticating matchScreenState = iota
	matchStateLoadingPlaylists
	matchStateList
	matchStateLoadingLibrary
	matchStateMatching
	matchStateDone
	matchStateFatal
)

// matchAction tells the root model what, if anything, it needs to do in
// response to something the match screen can't handle on its own.
type matchAction int

const (
	matchActionNone matchAction = iota
	matchActionBack
)

// matchStatusFilter narrows the playlist list to those that do/don't
// already have a written .m3u8 — cycled with "m" — so a library-wide catch
// up ("everything still missing a file") or a spot-check ("everything
// already synced") is a keypress away instead of eyeballing the whole list.
type matchStatusFilter int

const (
	matchFilterAll matchStatusFilter = iota
	matchFilterMissing
	matchFilterHasFile
)

func (f matchStatusFilter) next() matchStatusFilter {
	return (f + 1) % 3
}

func (f matchStatusFilter) label() string {
	switch f {
	case matchFilterMissing:
		return "Missing a file"
	case matchFilterHasFile:
		return "Has a file"
	default:
		return "All"
	}
}

// matchMethodFilter narrows the Done-screen results table to a chosen
// subset of match methods — e.g. fuzzy+missing together, to inspect every
// track that isn't a clean isrc hit — by toggling each category
// independently rather than cycling through fixed presets like
// matchStatusFilter does. Default (filterAllMethods) shows everything.
type matchMethodFilter uint8

const (
	filterISRC matchMethodFilter = 1 << iota
	filterFuzzy
	filterManual
	filterMissing

	filterAllMethods = filterISRC | filterFuzzy | filterManual | filterMissing
)

func (f matchMethodFilter) has(bit matchMethodFilter) bool                  { return f&bit != 0 }
func (f matchMethodFilter) toggled(bit matchMethodFilter) matchMethodFilter { return f ^ bit }

// methodBit maps a match.Method to its matchMethodFilter bit.
func methodBit(method match.Method) matchMethodFilter {
	switch method {
	case match.MethodISRC:
		return filterISRC
	case match.MethodFuzzy:
		return filterFuzzy
	case match.MethodManual:
		return filterManual
	default:
		return filterMissing
	}
}

// audioExtensions gates which files the manual-match file picker lets the
// user select — broad enough to cover this library without needing a
// filesystem scan to determine what's audio. Anything else (and every
// folder) still browses fine, just can't be picked.
var audioExtensions = []string{".mp3", ".flac", ".m4a", ".ogg", ".opus", ".wav", ".aac", ".wma", ".alac", ".aiff"}

// playlistRun holds one playlist's live match state for this batch run: the
// full playlist (kept around so a manual correction can re-render the
// .m3u8 without re-fetching anything) plus its per-track results, mutated
// in place when the user picks a different file for a track.
type playlistRun struct {
	playlist *spotifyapi.FullPlaylist
	results  []match.Result
	outcome  playlistOutcome
}

// trackRef locates one match.Result inside runs — what each results-table
// row actually points at, so editing a row can mutate the real result (and
// rewrite its playlist's .m3u8) instead of a display-only copy.
type trackRef struct {
	runIdx    int
	resultIdx int
}

// playlistOutcome summarizes one playlist's match+write run, for the Done
// screen.
type playlistOutcome struct {
	name          string
	dir           string
	matched       int
	total         int
	err           error
	navidromeSync string // non-empty: cover art/description not synced to Navidrome, and why
}

// MatchModel is the "Match to Local Library" screen: it owns login, the
// playlist browser, loading the Navidrome-backed library index, and the
// batch match+write run with its live results table.
type MatchModel struct {
	ctx        context.Context
	cfg        *config.Config
	client     *spotifyapi.Client
	httpClient *http.Client

	// library is loaded once per run (on first use) and reused for
	// subsequent match runs in the same session.
	library *library.Index

	// playlistGroups holds per-playlist Navidrome-group overrides (see
	// config.PlaylistGroups), loaded once at startup and edited from the
	// playlist list.
	playlistGroups *config.PlaylistGroups

	// manualMatches holds manual match corrections (see
	// config.ManualMatches), loaded once at startup, applied at the start
	// of every match run (ahead of automatic matching — see
	// match.Options.ManualOverrides), and updated whenever the user fixes
	// a match from the results screen.
	manualMatches *config.ManualMatches

	state matchScreenState

	spin spinner.Model
	list list.Model
	prog progress.Model
	tbl  table.Model

	width, height, contentHeight int

	user *spotifyapi.User
	err  error

	// allPlaylists is every loaded playlist, independent of what
	// statusFilter currently narrows m.list down to — selections (space/a)
	// and hasFile/group are tracked here so they survive switching filters,
	// and startMatching's queue is built from this, not the filtered view.
	allPlaylists []playlistItem
	statusFilter matchStatusFilter

	// Inline "edit this playlist's Navidrome group" text field, active
	// only in matchStateList.
	editingGroup bool
	groupInput   textinput.Model

	libEvents chan libraryLoadEvent
	libPhase  string
	libDone   int
	libTotal  int

	matchEvents   chan matchEvent
	currentStatus string
	runs          []*playlistRun
	queueLen      int
	queueDone     int

	// allTrackRefs is every matched track in this batch, independent of
	// methodFilter; trackRefs is the currently-visible (filtered) subset
	// actually shown in the table — every row-index-based lookup
	// (m.tbl.Cursor() into m.trackRefs, methodAtRow, resultAt, ...) reads
	// trackRefs, so filtering only ever affects what's on screen.
	allTrackRefs []trackRef
	trackRefs    []trackRef
	methodFilter matchMethodFilter

	// Manual-match file picker overlay, active only in matchStateDone.
	editingFile bool
	editingRef  trackRef
	editingRoot string
	editErr     string
	fp          filepicker.Model

	// Lidarr overlay (see lidarr_overlay.go), active only in
	// matchStateDone, and what it's done to each track this session.
	lidarr      lidarrOverlay
	lidarrNotes map[trackRef]string
}

func newMatchModel(cfg *config.Config) MatchModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(spotifyGreen)

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(spotifyGreen).BorderLeftForeground(spotifyGreen)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(spotifyGreen).BorderLeftForeground(spotifyGreen)

	l := list.New(nil, delegate, 0, 0)
	l.Title = "Match Playlists to Local Library"
	l.Styles.Title = titleStyle
	l.SetShowStatusBar(true)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	pg := progress.New(progress.WithGradient("#1ED760", "#1DB954"))

	tbl := table.New(table.WithFocused(true))
	tbl.SetStyles(tableStyles())

	groups, _ := config.LoadPlaylistGroups(cfg.PlaylistGroupsPath)      // usable even on error, see LoadPlaylistGroups
	manualMatches, _ := config.LoadManualMatches(cfg.ManualMatchesPath) // same

	gi := textinput.New()
	gi.Placeholder = cfg.NavidromeGroup
	gi.CharLimit = 200
	gi.Width = 40
	gi.PromptStyle = accentStyle
	gi.Cursor.Style = accentStyle

	return MatchModel{cfg: cfg, spin: sp, list: l, prog: pg, tbl: tbl, playlistGroups: groups, manualMatches: manualMatches, groupInput: gi, methodFilter: filterAllMethods}
}

// newLibraryFilePicker builds a fresh, library-scoped file picker rooted at
// root. Built fresh on every edit rather than reused: several of
// filepicker.Model's cursor/navigation-stack fields are unexported, so
// there's no way to reset a previously-used one back to a clean state from
// outside the package.
func newLibraryFilePicker(root string, height int) filepicker.Model {
	fp := filepicker.New()
	fp.CurrentDirectory = root
	fp.AllowedTypes = audioExtensions
	fp.FileAllowed = true
	fp.DirAllowed = false
	fp.ShowHidden = false
	fp.AutoHeight = false
	fp.SetHeight(height)
	fp.Styles.Cursor = fp.Styles.Cursor.Foreground(accent)
	fp.Styles.Directory = fp.Styles.Directory.Foreground(accent)
	fp.Styles.Selected = fp.Styles.Selected.Foreground(spotifyGreen).Bold(true)
	return fp
}

func matchMethodLabel(method match.Method) string {
	switch method {
	case match.MethodISRC:
		return "✓ isrc"
	case match.MethodFuzzy:
		return "~ fuzzy"
	case match.MethodManual:
		return "✎ manual"
	default:
		return "✗ missing"
	}
}

func methodRowColor(method match.Method) lipgloss.AdaptiveColor {
	switch method {
	case match.MethodISRC:
		return rowBgISRC
	case match.MethodFuzzy:
		return rowBgFuzzy
	case match.MethodManual:
		return rowBgManual
	default:
		return rowBgMissing
	}
}

// renderMatchTable draws tbl's visible rows itself instead of calling
// tbl.View(): bubbles/table (v1.0.0) has no per-row styling hook, and
// there's no safe way to bolt one on from outside the package — its cell
// rendering truncates by counting raw bytes with no awareness of ANSI
// escapes, so pre-coloring a cell's text and letting table's own Truncate
// run over it corrupts the escape sequences instead of the visible text.
// This mirrors table's own cell-composition pipeline (same
// Width/MaxWidth/Inline/Truncate treatment per cell) closely enough to be a
// drop-in replacement for tbl.View(), just with a method-colored background
// baked into each row's own cells rather than wrapped around the row after
// the fact (which would suffer the same embedded-reset problem: each
// cell's own style already ends in its own ANSI reset, so a background
// applied only around the outside would just get wiped by the first one).
//
// The visible row window is computed directly (cursor kept in view,
// clamped at both ends) rather than by copying table's own start/end
// formula — that one (verified against its source) is an *overscan* window
// up to 2×Height rows, meant to be fed through table's internal
// viewport.Model so its unexported YOffset can clip it down to exactly
// Height visible rows. There's no way to read that offset from outside the
// package, and rendering the overscan window unclipped, as this used to
// do, produced up to 2× too many rows at some cursor positions — tall
// enough to overflow a real terminal, which then scrolled on its own and
// cut the chrome above the table off-screen.
func renderMatchTable(tbl table.Model, methodAt func(row int) match.Method) string {
	cols := tbl.Columns()
	rows := tbl.Rows()
	cursor := tbl.Cursor()
	height := tbl.Height()
	if height < 1 {
		height = 1
	}
	sty := tableStyles()

	renderCell := func(style lipgloss.Style, value string, width int) string {
		boxed := lipgloss.NewStyle().Width(width).MaxWidth(width).Inline(true).Render(runewidth.Truncate(value, width, "…"))
		return style.Render(boxed)
	}

	var header []string
	for _, c := range cols {
		if c.Width <= 0 {
			continue
		}
		header = append(header, renderCell(sty.Header, c.Title, c.Width))
	}

	start := 0
	if len(rows) > height {
		start = clampInt(cursor-height/2, 0, len(rows)-height)
	}
	end := clampInt(start+height, start, len(rows))

	lines := []string{lipgloss.JoinHorizontal(lipgloss.Top, header...)}
	for i := start; i < end; i++ {
		cellStyle := sty.Cell
		if i != cursor {
			cellStyle = cellStyle.Background(methodRowColor(methodAt(i)))
		}
		var cells []string
		for c, value := range rows[i] {
			if cols[c].Width <= 0 {
				continue
			}
			cells = append(cells, renderCell(cellStyle, value, cols[c].Width))
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cells...)
		if i == cursor {
			row = sty.Selected.Render(row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SetSize propagates a terminal resize to every widget this screen owns.
func (m *MatchModel) SetSize(width, height int) {
	m.width, m.height = width, height
	m.contentHeight = height - 6
	if m.contentHeight < 5 {
		m.contentHeight = 5
	}

	listW := (width * 3) / 5
	if listW < 30 {
		listW = width
	}
	// -2: the status-filter chip row viewList draws above the list, plus
	// its blank-line separator.
	listHeight := m.contentHeight - 2
	if listHeight < 3 {
		listHeight = 3
	}
	m.list.SetSize(listW, listHeight)

	m.prog.Width = width - 4
	if m.prog.Width < 10 {
		m.prog.Width = 10
	}

	m.tbl.SetWidth(width)
	m.tbl.SetHeight(m.contentHeight)
	m.tbl.SetColumns(matchTableColumns(width))

	m.fp.SetHeight(m.contentHeight)
}

func matchTableColumns(width int) []table.Column {
	playlistW, methodW := 16, 10
	rest := width - playlistW - methodW - 10
	if rest < 30 {
		rest = 30
	}
	trackW := rest * 55 / 100
	noteW := rest - trackW
	return []table.Column{
		{Title: "Playlist", Width: playlistW},
		{Title: "Track", Width: trackW},
		{Title: "Method", Width: methodW},
		{Title: "Match", Width: noteW},
	}
}

// Enter (re)starts this screen: authenticating first if there's no cached
// client yet, otherwise going straight to loading the playlist list.
func (m MatchModel) Enter(ctx context.Context) (MatchModel, tea.Cmd) {
	m.ctx = ctx
	m.err = nil

	if m.client != nil {
		m.state = matchStateLoadingPlaylists
		return m, tea.Batch(m.spin.Tick, loadPlaylists(ctx, m.client))
	}
	m.state = matchStateAuthenticating
	return m, tea.Batch(m.spin.Tick, authenticate(ctx, m.cfg))
}

// InvalidateClient discards any cached authenticated client, e.g. after the
// user changes credentials or logs out in Settings.
func (m *MatchModel) InvalidateClient() { m.client = nil }

// Keys returns the keymap currently in effect, for the shared help bar.
func (m MatchModel) Keys() help.KeyMap {
	switch m.state {
	case matchStateList:
		if m.editingGroup {
			return settingsEditKeys
		}
		if m.list.FilterState() == list.Filtering {
			return m.list
		}
		return matchListKeys
	case matchStateMatching, matchStateLoadingLibrary:
		return exportRunKeys
	case matchStateDone:
		if m.lidarr.active() {
			return m.lidarrHelpKeys()
		}
		if m.editingFile {
			return matchEditKeys
		}
		return matchDoneKeys
	case matchStateFatal:
		return exportDoneKeys
	default:
		return exportRunKeys
	}
}

// IsFiltering reports whether the playlist list's text filter currently
// has focus.
func (m MatchModel) IsFiltering() bool {
	return m.state == matchStateList && m.list.FilterState() == list.Filtering
}

// IsEditingGroup reports whether the inline Navidrome-group text field
// currently has focus, so the root model knows to route every keystroke
// into it instead of treating them as shortcuts.
func (m MatchModel) IsEditingGroup() bool {
	return m.state == matchStateList && m.editingGroup
}

func (m MatchModel) Update(msg tea.Msg) (MatchModel, tea.Cmd, matchAction) {
	switch msg := msg.(type) {

	case authDoneMsg:
		if msg.err != nil {
			m.state = matchStateFatal
			m.err = msg.err
			return m, nil, matchActionNone
		}
		m.client = spotifyapi.New(msg.httpClient)
		m.httpClient = msg.httpClient
		m.state = matchStateLoadingPlaylists
		return m, tea.Batch(m.spin.Tick, loadPlaylists(m.ctx, m.client)), matchActionNone

	case errMsg:
		m.state = matchStateFatal
		m.err = msg.err
		return m, nil, matchActionNone

	case playlistsLoadedMsg:
		m.user = msg.user
		items := make([]playlistItem, len(msg.playlists))
		for i, p := range msg.playlists {
			items[i] = playlistItem{
				playlist: p,
				hasFile:  m.playlistHasFile(p.Name),
				group:    resolveGroup(m.cfg, m.playlistGroups, p.ID),
			}
		}
		m.allPlaylists = items
		m.statusFilter = matchFilterAll
		m.applyStatusFilter()
		m.state = matchStateList
		return m, nil, matchActionNone

	case lidarrCandidatesMsg, lidarrAppliedMsg, lidarrBulkEvent:
		m, cmd, _ := m.handleLidarrMsg(msg)
		return m, cmd, matchActionNone

	case spinner.TickMsg:
		if m.state != matchStateAuthenticating && m.state != matchStateLoadingPlaylists && m.state != matchStateLoadingLibrary && !m.lidarr.busy() {
			return m, nil, matchActionNone
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd, matchActionNone

	case progress.FrameMsg:
		newModel, cmd := m.prog.Update(msg)
		m.prog = newModel.(progress.Model)
		return m, cmd, matchActionNone

	case libraryLoadEvent:
		return m.handleLibraryEvent(msg)

	case matchEvent:
		return m.handleMatchEvent(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	switch m.state {
	case matchStateList:
		if m.editingGroup {
			var cmd tea.Cmd
			m.groupInput, cmd = m.groupInput.Update(msg)
			return m, cmd, matchActionNone
		}
		if d := wheelDelta(msg); d != 0 {
			scrollList(&m.list, d)
			return m, nil, matchActionNone
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd, matchActionNone
	case matchStateMatching:
		if d := wheelDelta(msg); d != 0 {
			scrollTable(&m.tbl, d)
			return m, nil, matchActionNone
		}
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd, matchActionNone
	case matchStateDone:
		if m.lidarr.active() {
			return m, nil, matchActionNone
		}
		if m.editingFile {
			// Forwards everything, notably the file picker's own
			// unexported readDirMsg it sends itself after Init()/opening a
			// folder — without this it would never learn what's in the
			// directory it just navigated to.
			var cmd tea.Cmd
			m.fp, cmd = m.fp.Update(msg)
			return m, cmd, matchActionNone
		}
		if d := wheelDelta(msg); d != 0 {
			scrollTable(&m.tbl, d)
			return m, nil, matchActionNone
		}
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd, matchActionNone
	}
	return m, nil, matchActionNone
}

func (m MatchModel) handleKey(msg tea.KeyMsg) (MatchModel, tea.Cmd, matchAction) {
	if m.state == matchStateList && m.editingGroup {
		return m.handleGroupEditKey(msg)
	}
	if m.state == matchStateList && m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd, matchActionNone
	}

	switch m.state {
	case matchStateList:
		switch {
		case key.Matches(msg, matchListKeys.Back):
			return m, nil, matchActionBack
		case key.Matches(msg, matchListKeys.Toggle):
			idx := m.list.GlobalIndex() // SetItem indexes into the unfiltered list; Index() doesn't when a text filter is active
			if it, ok := m.list.SelectedItem().(playlistItem); ok {
				it.selected = !it.selected
				m.list.SetItem(idx, it)
				m.syncAllPlaylist(it)
			}
			return m, nil, matchActionNone
		case key.Matches(msg, matchListKeys.SelectAll):
			items := m.list.Items()
			allSelected := true
			for _, it := range items {
				if !it.(playlistItem).selected {
					allSelected = false
					break
				}
			}
			for i, it := range items {
				p := it.(playlistItem)
				p.selected = !allSelected
				m.list.SetItem(i, p)
				m.syncAllPlaylist(p)
			}
			return m, nil, matchActionNone
		case key.Matches(msg, matchListKeys.StatusFilter):
			m.statusFilter = m.statusFilter.next()
			m.applyStatusFilter()
			return m, nil, matchActionNone
		case key.Matches(msg, matchListKeys.EditGroup):
			return m.beginEditGroup()
		case key.Matches(msg, matchListKeys.Match):
			return m.startOrLoadLibrary()
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd, matchActionNone

	case matchStateLoadingLibrary, matchStateMatching:
		if key.Matches(msg, exportRunKeys.Cancel) {
			return m, nil, matchActionBack
		}
		if m.state == matchStateMatching {
			var cmd tea.Cmd
			m.tbl, cmd = m.tbl.Update(msg)
			return m, cmd, matchActionNone
		}
		return m, nil, matchActionNone

	case matchStateAuthenticating, matchStateLoadingPlaylists:
		if key.Matches(msg, exportRunKeys.Cancel) {
			return m, nil, matchActionBack
		}
		return m, nil, matchActionNone

	case matchStateDone:
		if m.lidarr.active() {
			return m.handleLidarrKey(msg)
		}
		if m.editingFile {
			return m.handleFilePickerKey(msg)
		}
		switch {
		case key.Matches(msg, matchDoneKeys.Back):
			return m, nil, matchActionBack
		case key.Matches(msg, matchDoneKeys.Edit):
			return m.beginEdit()
		case key.Matches(msg, matchDoneKeys.Lidarr):
			return m.beginLidarr()
		case key.Matches(msg, matchDoneKeys.LidarrAll):
			return m.beginLidarrBulk()
		case key.Matches(msg, matchDoneKeys.FilterISRC):
			m.methodFilter = m.methodFilter.toggled(filterISRC)
			m.applyMethodFilter()
			return m, nil, matchActionNone
		case key.Matches(msg, matchDoneKeys.FilterFuzzy):
			m.methodFilter = m.methodFilter.toggled(filterFuzzy)
			m.applyMethodFilter()
			return m, nil, matchActionNone
		case key.Matches(msg, matchDoneKeys.FilterManual):
			m.methodFilter = m.methodFilter.toggled(filterManual)
			m.applyMethodFilter()
			return m, nil, matchActionNone
		case key.Matches(msg, matchDoneKeys.FilterMissing):
			m.methodFilter = m.methodFilter.toggled(filterMissing)
			m.applyMethodFilter()
			return m, nil, matchActionNone
		case key.Matches(msg, matchDoneKeys.FilterAll):
			m.methodFilter = filterAllMethods
			m.applyMethodFilter()
			return m, nil, matchActionNone
		}
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd, matchActionNone

	case matchStateFatal:
		if key.Matches(msg, exportDoneKeys.Continue) {
			return m, nil, matchActionBack
		}
		return m, nil, matchActionNone
	}

	return m, nil, matchActionNone
}

// beginEdit opens the file-picker overlay, scoped to the configured
// Navidrome music root, for the track currently selected in the results
// table.
func (m MatchModel) beginEdit() (MatchModel, tea.Cmd, matchAction) {
	cursor := m.tbl.Cursor()
	if cursor < 0 || cursor >= len(m.trackRefs) {
		return m, nil, matchActionNone
	}

	m.editingRef = m.trackRefs[cursor]
	m.editingRoot = filepath.Clean(m.cfg.NavidromeMusicPath)
	m.editingFile = true
	m.editErr = ""
	m.fp = newLibraryFilePicker(m.editingRoot, m.contentHeight)

	return m, m.fp.Init(), matchActionNone
}

// handleFilePickerKey drives the file-picker overlay. It intercepts the
// picker's own "go back a directory" keys (which include esc) right at the
// scoped root: bubbles/filepicker has no notion of a floor, so left to
// itself it would happily walk above the library root the picker is
// supposed to be confined to. At the root, those same keys instead cancel
// the edit and close the overlay.
func (m MatchModel) handleFilePickerKey(msg tea.KeyMsg) (MatchModel, tea.Cmd, matchAction) {
	if key.Matches(msg, m.fp.KeyMap.Back) && m.fp.CurrentDirectory == m.editingRoot {
		m.editingFile = false
		m.editErr = ""
		return m, nil, matchActionNone
	}

	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)

	if didSelect, path := m.fp.DidSelectFile(msg); didSelect {
		return m.applyManualMatch(path)
	}
	if didSelectDisabled, _ := m.fp.DidSelectDisabledFile(msg); didSelectDisabled {
		m.editErr = "that file type isn't supported"
	}

	return m, cmd, matchActionNone
}

// applyManualMatch records the user's chosen file as the track's match,
// persists it immediately (rewriting just that playlist's .m3u8 and
// missing.txt — no network, no cover re-download) and separately (keyed by
// Spotify track ID, in manualMatches) so the correction survives a later
// from-scratch re-match instead of being silently recomputed away, and
// closes the overlay.
func (m MatchModel) applyManualMatch(path string) (MatchModel, tea.Cmd, matchAction) {
	m.editingFile = false
	m.editErr = ""

	run := m.runs[m.editingRef.runIdx]
	r := &run.results[m.editingRef.resultIdx]
	r.Method = match.MethodManual
	r.LocalPath = path
	r.Confidence = 1

	if r.Item.Track != nil {
		if err := m.manualMatches.Set(r.Item.Track.ID, path); err != nil {
			m.editErr = "remembering this match for next time: " + err.Error()
		}
	}

	group := resolveGroup(m.cfg, m.playlistGroups, run.playlist.ID)
	if res, err := m3u8.Rewrite(m.cfg.M3U8Dir, run.playlist, run.results, group); err != nil {
		m.editErr = "saving playlist: " + err.Error()
	} else {
		run.outcome.matched = res.Matched
		run.outcome.dir = res.Dir
	}

	m.tbl.SetRows(m.tableRows())
	return m, nil, matchActionNone
}

// playlistHasFile reports whether name already has a written .m3u8 under
// the configured output directory.
func (m MatchModel) playlistHasFile(name string) bool {
	if m.cfg.M3U8Dir == "" {
		return false
	}
	_, err := os.Stat(m3u8.M3U8Path(m.cfg.M3U8Dir, name))
	return err == nil
}

// syncAllPlaylist writes it back into m.allPlaylists (matched by playlist
// ID), so a selection toggle made while a status filter narrows the
// visible list isn't lost when the filter changes again — m.list only ever
// holds a subset, m.allPlaylists is the durable source of truth.
func (m *MatchModel) syncAllPlaylist(it playlistItem) {
	for i := range m.allPlaylists {
		if m.allPlaylists[i].playlist.ID == it.playlist.ID {
			m.allPlaylists[i] = it
			return
		}
	}
}

// applyStatusFilter rebuilds m.list's visible items from m.allPlaylists
// according to m.statusFilter.
func (m *MatchModel) applyStatusFilter() {
	visible := make([]list.Item, 0, len(m.allPlaylists))
	for _, it := range m.allPlaylists {
		switch m.statusFilter {
		case matchFilterMissing:
			if it.hasFile {
				continue
			}
		case matchFilterHasFile:
			if !it.hasFile {
				continue
			}
		}
		visible = append(visible, it)
	}
	m.list.SetItems(visible)
}

// beginEditGroup opens the inline Navidrome-group text field for the
// playlist currently highlighted in the list.
func (m MatchModel) beginEditGroup() (MatchModel, tea.Cmd, matchAction) {
	it, ok := m.list.SelectedItem().(playlistItem)
	if !ok {
		return m, nil, matchActionNone
	}
	m.editingGroup = true
	m.groupInput.Placeholder = m.cfg.NavidromeGroup
	if override, has := m.playlistGroups.Get(it.playlist.ID); has {
		m.groupInput.SetValue(override)
	} else {
		m.groupInput.SetValue("")
	}
	m.groupInput.CursorEnd()
	cmd := m.groupInput.Focus()
	return m, cmd, matchActionNone
}

// handleGroupEditKey drives the inline Navidrome-group text field: enter
// saves (an empty value clears the override, reverting to the global
// default) and esc cancels without saving.
func (m MatchModel) handleGroupEditKey(msg tea.KeyMsg) (MatchModel, tea.Cmd, matchAction) {
	switch {
	case key.Matches(msg, settingsEditKeys.Confirm) && msg.String() == "esc":
		m.groupInput.Blur()
		m.editingGroup = false
		return m, nil, matchActionNone

	case key.Matches(msg, settingsEditKeys.Confirm):
		idx := m.list.GlobalIndex() // SetItem indexes into the unfiltered list; Index() doesn't when a text filter is active
		it, ok := m.list.SelectedItem().(playlistItem)
		if !ok {
			m.editingGroup = false
			return m, nil, matchActionNone
		}
		value := strings.TrimSpace(m.groupInput.Value())
		if err := m.playlistGroups.Set(it.playlist.ID, value); err != nil {
			m.groupInput.Blur()
			m.editingGroup = false
			return m, nil, matchActionNone
		}
		it.group = resolveGroup(m.cfg, m.playlistGroups, it.playlist.ID)
		m.list.SetItem(idx, it)
		m.syncAllPlaylist(it)
		m.groupInput.Blur()
		m.editingGroup = false
		return m, nil, matchActionNone
	}

	var cmd tea.Cmd
	m.groupInput, cmd = m.groupInput.Update(msg)
	return m, cmd, matchActionNone
}

func (m MatchModel) startOrLoadLibrary() (MatchModel, tea.Cmd, matchAction) {
	if m.library != nil {
		return m.startMatching()
	}

	m.state = matchStateLoadingLibrary
	m.libPhase = ""
	m.libDone, m.libTotal = 0, 0

	ch := make(chan libraryLoadEvent)
	m.libEvents = ch
	go runLibraryLoad(m.ctx, m.cfg, ch)

	return m, tea.Batch(m.spin.Tick, waitForLibraryEvent(ch)), matchActionNone
}

func (m MatchModel) handleLibraryEvent(ev libraryLoadEvent) (MatchModel, tea.Cmd, matchAction) {
	if !ev.Final {
		m.libPhase = string(ev.Progress.Phase)
		m.libDone = ev.Progress.Done
		m.libTotal = ev.Progress.Total
		return m, waitForLibraryEvent(m.libEvents), matchActionNone
	}

	if ev.Err != nil {
		m.state = matchStateFatal
		m.err = fmt.Errorf("loading local library: %w", ev.Err)
		return m, nil, matchActionNone
	}

	m.library = ev.Index
	return m.startMatching()
}

func (m MatchModel) startMatching() (MatchModel, tea.Cmd, matchAction) {
	// Selections are checked against the full set, not just m.list's
	// current (possibly status-filtered) view — a playlist checked while
	// filtered to "Missing" stays queued even after switching back to
	// "All", since the checkbox represents a persistent choice, not one
	// scoped to whatever's visible right now.
	var queue []spotifyapi.SimplifiedPlaylist
	for _, p := range m.allPlaylists {
		if p.selected {
			queue = append(queue, p.playlist)
		}
	}
	if len(queue) == 0 {
		if cur, ok := m.list.SelectedItem().(playlistItem); ok {
			queue = []spotifyapi.SimplifiedPlaylist{cur.playlist}
		}
	}
	if len(queue) == 0 {
		m.state = matchStateList
		return m, nil, matchActionNone
	}

	m.runs = nil
	m.allTrackRefs = nil
	m.trackRefs = nil
	m.methodFilter = filterAllMethods
	m.lidarrNotes = nil
	m.currentStatus = ""
	m.queueLen = len(queue)
	m.queueDone = 0
	m.state = matchStateMatching
	m.tbl.SetColumns(matchTableColumns(m.width))
	m.tbl.SetRows(nil)

	ch := make(chan matchEvent)
	m.matchEvents = ch
	go runMatch(m.ctx, m.client, m.httpClient, m.library, m.cfg, m.playlistGroups, m.manualMatches, queue, ch)

	return m, tea.Batch(waitForMatchEvent(ch), m.prog.SetPercent(0)), matchActionNone
}

func (m MatchModel) handleMatchEvent(ev matchEvent) (MatchModel, tea.Cmd, matchAction) {
	switch ev.kind {
	case matchEventStatus:
		m.currentStatus = ev.text
		return m, waitForMatchEvent(m.matchEvents), matchActionNone

	case matchEventPlaylistDone:
		m.currentStatus = ""
		m.queueDone++
		outcome := playlistOutcome{name: ev.playlistName, total: len(ev.results), navidromeSync: ev.syncWarn}
		if ev.err != nil {
			outcome.err = ev.err
		}
		if ev.write != nil {
			outcome.dir = ev.write.Dir
			outcome.matched = ev.write.Matched
		}

		runIdx := len(m.runs)
		m.runs = append(m.runs, &playlistRun{playlist: ev.playlist, results: ev.results, outcome: outcome})
		for i, r := range ev.results {
			if r.Item.Track == nil {
				continue
			}
			m.allTrackRefs = append(m.allTrackRefs, trackRef{runIdx: runIdx, resultIdx: i})
		}
		m.applyMethodFilter()
		m.tbl.GotoBottom()

		pct := float64(m.queueDone) / float64(m.queueLen)
		cmd := m.prog.SetPercent(pct)
		return m, tea.Batch(cmd, waitForMatchEvent(m.matchEvents)), matchActionNone

	case matchEventAllDone:
		m.state = matchStateDone
		if len(m.trackRefs) > 0 {
			m.tbl.SetCursor(0)
		}
		return m, nil, matchActionNone
	}
	return m, nil, matchActionNone
}

// applyMethodFilter rebuilds trackRefs — the table's visible rows — from
// allTrackRefs according to methodFilter, and feeds the result to the
// table. table.SetRows re-clamps the cursor on its own if the row count
// shrank past it, but only downward — if a filter toggle passes through
// zero visible rows (e.g. missing-only, then toggling missing off before
// toggling fuzzy on), the cursor goes to -1 and SetRows never brings it
// back even once rows exist again, leaving nothing selected. Fix that up
// explicitly.
func (m *MatchModel) applyMethodFilter() {
	if m.methodFilter == filterAllMethods {
		m.trackRefs = m.allTrackRefs
	} else {
		m.trackRefs = make([]trackRef, 0, len(m.allTrackRefs))
		for _, ref := range m.allTrackRefs {
			method := m.runs[ref.runIdx].results[ref.resultIdx].Method
			if m.methodFilter.has(methodBit(method)) {
				m.trackRefs = append(m.trackRefs, ref)
			}
		}
	}
	m.tbl.SetRows(m.tableRows())
	if len(m.trackRefs) > 0 && m.tbl.Cursor() < 0 {
		m.tbl.SetCursor(0)
	}
}

// methodAtRow returns the match method for the given results-table row
// index, used to color that row's background in renderMatchTable.
func (m MatchModel) methodAtRow(row int) match.Method {
	if row < 0 || row >= len(m.trackRefs) {
		return match.MethodNone
	}
	ref := m.trackRefs[row]
	return m.runs[ref.runIdx].results[ref.resultIdx].Method
}

// resultAt returns the playlist name and match result for the given table
// row index, if valid.
func (m MatchModel) resultAt(row int) (playlistName string, r match.Result, ok bool) {
	if row < 0 || row >= len(m.trackRefs) {
		return "", match.Result{}, false
	}
	ref := m.trackRefs[row]
	run := m.runs[ref.runIdx]
	return run.playlist.Name, run.results[ref.resultIdx], true
}

func (m MatchModel) tableRows() []table.Row {
	out := make([]table.Row, len(m.trackRefs))
	for i, ref := range m.trackRefs {
		run := m.runs[ref.runIdx]
		r := run.results[ref.resultIdx]

		track := ""
		if r.Item.Track != nil {
			track = r.Item.Track.Name
			if artist := artistList(r.Item.Track.Artists); artist != "" {
				track = artist + " – " + track
			}
		}

		note := ""
		switch {
		case r.LocalPath != "":
			note = filepath.Base(r.LocalPath)
		case run.outcome.err != nil:
			note = "error: " + run.outcome.err.Error()
		}

		out[i] = table.Row{run.playlist.Name, track, matchMethodLabel(r.Method), note}
	}
	return out
}

func artistList(artists []spotifyapi.Artist) string {
	names := make([]string, len(artists))
	for i, a := range artists {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

func (m MatchModel) View() string {
	switch m.state {
	case matchStateAuthenticating:
		return fmt.Sprintf("\n  %s Waiting for Spotify login in your browser...\n", m.spin.View())

	case matchStateLoadingPlaylists:
		return fmt.Sprintf("\n  %s Loading your Spotify playlists...\n", m.spin.View())

	case matchStateList:
		return m.viewList()

	case matchStateLoadingLibrary:
		line := fmt.Sprintf("\n  %s Loading your local library from Navidrome...\n", m.spin.View())
		if m.libPhase != "" && m.libDone > 0 {
			line += fmt.Sprintf("\n  %s: %d tracks\n", m.libPhase, m.libDone)
		}
		return line

	case matchStateMatching:
		return m.viewBatch(fmt.Sprintf("Matching %d/%d playlists...", m.queueDone, m.queueLen))

	case matchStateDone:
		if m.lidarr.active() {
			return m.viewLidarr()
		}
		if m.editingFile {
			return m.viewFilePicker()
		}
		return m.viewDone()

	case matchStateFatal:
		return errorStyle.Render("Error: "+m.err.Error()) + "\n"
	}
	return ""
}

func (m MatchModel) viewBatch(heading string) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(heading) + "\n\n")
	b.WriteString(m.prog.View())
	b.WriteString("\n")
	if m.currentStatus != "" {
		b.WriteString(dimStyle.Render(m.currentStatus) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString(renderMatchTable(m.tbl, m.methodAtRow))
	return b.String()
}

// viewDone renders the finished results table alongside a detail panel for
// the currently selected track (full artist/album/path — the table itself
// only has room for a trimmed filename) and, below both, each playlist's
// write outcome.
func (m MatchModel) viewDone() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Done") + "  " + m.renderMethodChips() + "\n\n")

	tableW := (m.width * 3) / 5
	detailW := m.width - tableW - 3
	if detailW < 28 || m.width < 90 {
		m.tbl.SetWidth(m.width)
		m.tbl.SetColumns(matchTableColumns(m.width))
		m.tbl.SetRows(m.tableRows())
		b.WriteString(renderMatchTable(m.tbl, m.methodAtRow))
		b.WriteString("\n")
		b.WriteString(m.renderTrackDetail(m.width - 4))
	} else {
		m.tbl.SetWidth(tableW)
		m.tbl.SetColumns(matchTableColumns(tableW))
		m.tbl.SetRows(m.tableRows())
		detail := panelStyle.Width(detailW).Height(m.contentHeight - 2).Render(m.renderTrackDetail(detailW - 2))
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, renderMatchTable(m.tbl, m.methodAtRow), "  ", detail))
	}

	if m.editErr != "" {
		b.WriteString("\n" + errorStyle.Render(m.editErr))
	}
	b.WriteString("\n\n")
	b.WriteString(m.viewOutcomes())
	return b.String()
}

func (m MatchModel) renderTrackDetail(width int) string {
	playlistName, r, ok := m.resultAt(m.tbl.Cursor())
	if !ok || r.Item.Track == nil {
		return fadedStyle.Render("No track selected.")
	}
	t := r.Item.Track

	var b strings.Builder
	b.WriteString(panelTitleStyle.Render(t.Name) + "\n\n")

	row := func(label, value string) {
		if value == "" {
			return
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("%-9s", label)) + bodyStyle.Width(width-9).Render(value) + "\n")
	}

	row("Artist", artistList(t.Artists))
	row("Album", t.Album.Name)
	row("Playlist", playlistName)
	b.WriteString(dimStyle.Render(fmt.Sprintf("%-9s", "Method")) + methodDetailStyle(r.Method).Render(matchMethodLabel(r.Method)) + "\n")

	b.WriteString("\n" + dimStyle.Render("Path") + "\n")
	if r.LocalPath != "" {
		b.WriteString(bodyStyle.Width(width).Render(r.LocalPath) + "\n")
	} else {
		b.WriteString(fadedStyle.Render("(not matched)") + "\n")
	}

	if note, ok := m.lidarrNotes[m.trackRefs[m.tbl.Cursor()]]; ok {
		b.WriteString("\n" + dimStyle.Render("Lidarr") + "\n")
		b.WriteString(accentStyle.Width(width).Render(note) + "\n")
	}

	b.WriteString("\n" + fadedStyle.Render("enter to fix this match · l add album to Lidarr"))
	return b.String()
}

func methodDetailStyle(method match.Method) lipgloss.Style {
	switch method {
	case match.MethodISRC:
		return badgeDone
	case match.MethodFuzzy:
		return warnStyle
	case match.MethodManual:
		return accentStyle
	default:
		return badgeFailed
	}
}

// viewFilePicker renders the manual-match overlay: which track is being
// fixed, up top, and the library-scoped file picker below it.
func (m MatchModel) viewFilePicker() string {
	playlistName, r, ok := m.resultAt(m.tbl.Cursor())

	var b strings.Builder
	b.WriteString(headerStyle.Render("Fix match") + "\n\n")
	if ok && r.Item.Track != nil {
		track := r.Item.Track.Name
		if artist := artistList(r.Item.Track.Artists); artist != "" {
			track = artist + " – " + track
		}
		b.WriteString(bodyStyle.Render(track) + "\n")
		if album := r.Item.Track.Album.Name; album != "" {
			// The whole point of showing this: it's what you're actually
			// hunting for in the picker below — the artist/track alone
			// doesn't tell you which of an artist's several albums to go
			// into.
			b.WriteString(dimStyle.Render("Album: ") + accentStyle.Render(album) + "\n")
		}
		b.WriteString(dimStyle.Render(playlistName) + "\n")
	}
	b.WriteString(fadedStyle.Render(m.editingRoot) + "\n\n")

	if m.editErr != "" {
		b.WriteString(errorStyle.Render(m.editErr) + "\n\n")
	}

	header := b.String()

	// The header above is a variable number of lines (whether there's an
	// album, an error, or long text that wraps at a narrow width), so the
	// picker's own height is set here, against however tall that header
	// actually rendered, rather than as a fixed guess in SetSize — a
	// static guess would risk the same overflow bug the results table had
	// (content taller than the terminal, which then scrolls on its own
	// and cuts the chrome off above it). Safe to set on every render:
	// filepicker.SetHeight only ever clamps its scroll window to fit, it
	// doesn't reset navigation state.
	m.fp.SetHeight(clampInt(m.contentHeight-lipgloss.Height(header), 5, m.contentHeight))

	return header + m.fp.View()
}

func (m MatchModel) viewOutcomes() string {
	var b strings.Builder
	for _, run := range m.runs {
		o := run.outcome
		if o.err != nil {
			b.WriteString(errorStyle.Render(fmt.Sprintf("✗ %s: %v", o.name, o.err)) + "\n")
			continue
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("%s: %d/%d matched → %s", o.name, o.matched, o.total, o.dir)) + "\n")
		if o.navidromeSync != "" {
			b.WriteString(warnStyle.Render(fmt.Sprintf("    Navidrome sync: %s", o.navidromeSync)) + "\n")
		}
	}
	return b.String()
}

func (m MatchModel) viewList() string {
	chips := m.renderStatusChips() + "\n\n"

	listW := (m.width * 3) / 5
	if listW < 30 {
		return chips + m.list.View()
	}
	detailW := m.width - listW - 6
	if detailW < 20 {
		return chips + m.list.View()
	}

	detail := panelStyle.Width(detailW).Height(m.contentHeight - 2).Render(m.renderDetail(detailW - 2))
	return chips + lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), "  ", detail)
}

// renderStatusChips shows how many loaded playlists already have a written
// .m3u8 vs. don't, with the active statusFilter highlighted — both a quick
// summary and a reminder that "m" cycles it.
func (m MatchModel) renderStatusChips() string {
	var hasFile, missing int
	for _, p := range m.allPlaylists {
		if p.hasFile {
			hasFile++
		} else {
			missing++
		}
	}

	chip := func(label string, count int, active bool) string {
		text := fmt.Sprintf(" %s (%d) ", label, count)
		if active {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(spotifyGreen).Bold(true).Render(text)
		}
		return dimStyle.Render(text)
	}

	return chip("All", len(m.allPlaylists), m.statusFilter == matchFilterAll) + " " +
		chip("Has file", hasFile, m.statusFilter == matchFilterHasFile) + " " +
		chip("Missing", missing, m.statusFilter == matchFilterMissing) + "   " +
		fadedStyle.Render("m to cycle")
}

// renderMethodChips is the results table's method filter: one toggle chip
// per match method, numbered to match the keys that flip them (1-4), with
// a live count of each (against allTrackRefs, so the counts don't change
// as the filter narrows the table down) and the active ones highlighted in
// their method's own color. Several can be on at once — e.g. 2+4 to
// inspect every fuzzy and missing track together — unlike the single-choice
// playlist-list filter above.
func (m MatchModel) renderMethodChips() string {
	var isrcN, fuzzyN, manualN, missN int
	for _, ref := range m.allTrackRefs {
		switch m.runs[ref.runIdx].results[ref.resultIdx].Method {
		case match.MethodISRC:
			isrcN++
		case match.MethodFuzzy:
			fuzzyN++
		case match.MethodManual:
			manualN++
		default:
			missN++
		}
	}

	chip := func(key, label string, count int, active bool, bg lipgloss.TerminalColor) string {
		text := fmt.Sprintf(" %s %s (%d) ", key, label, count)
		if active {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(bg).Bold(true).Render(text)
		}
		return fadedStyle.Render(text)
	}

	row := chip("1", "isrc", isrcN, m.methodFilter.has(filterISRC), spotifyGreen) + " " +
		chip("2", "fuzzy", fuzzyN, m.methodFilter.has(filterFuzzy), warnAmber) + " " +
		chip("3", "manual", manualN, m.methodFilter.has(filterManual), accent) + " " +
		chip("4", "missing", missN, m.methodFilter.has(filterMissing), errorRed)
	if m.methodFilter != filterAllMethods {
		row += "   " + fadedStyle.Render("0 show all")
	}
	return row
}

func (m MatchModel) renderDetail(width int) string {
	it, ok := m.list.SelectedItem().(playlistItem)
	if !ok {
		return fadedStyle.Render("No playlist selected.")
	}
	p := it.playlist

	var b strings.Builder
	b.WriteString(panelTitleStyle.Render(p.Name) + "\n\n")

	row := func(label, value string) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("%-11s", label)) + bodyStyle.Render(value) + "\n")
	}

	owner := p.Owner.DisplayName
	if owner == "" {
		owner = p.Owner.ID
	}
	row("Owner", owner)
	row("Tracks", accentStyle.Render(fmt.Sprintf("%d", p.Tracks.Total)))
	if it.hasFile {
		row("File", successStyle.Render("✓ written"))
	} else {
		row("File", fadedStyle.Render("not written yet"))
	}

	if p.Description != "" {
		b.WriteString("\n" + dimStyle.Render("Description") + "\n")
		b.WriteString(bodyStyle.Width(width).Render(p.Description) + "\n")
	}

	b.WriteString("\n" + dimStyle.Render("Navidrome group") + "\n")
	if m.editingGroup {
		b.WriteString(m.groupInput.View() + "\n")
		b.WriteString(fadedStyle.Render("enter to save (blank clears override) · esc to cancel") + "\n")
	} else {
		groupLine := bodyStyle.Render(it.group)
		if _, custom := m.playlistGroups.Get(p.ID); custom {
			groupLine += "  " + accentStyle.Render("(custom)")
		}
		b.WriteString(groupLine + "\n")
	}

	b.WriteString("\n")
	if it.selected {
		b.WriteString(successStyle.Render("✓ selected for matching"))
	} else {
		b.WriteString(fadedStyle.Render("space to select for batch matching"))
	}
	if !m.editingGroup {
		b.WriteString("  ·  " + fadedStyle.Render("g to edit group"))
	}

	return b.String()
}
