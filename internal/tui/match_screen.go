package tui

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

	state matchScreenState

	spin spinner.Model
	list list.Model
	prog progress.Model
	tbl  table.Model

	width, height, contentHeight int

	user *spotifyapi.User
	err  error

	libEvents chan libraryLoadEvent
	libPhase  string
	libDone   int
	libTotal  int

	matchEvents   chan matchEvent
	currentStatus string
	runs          []*playlistRun
	trackRefs     []trackRef
	queueLen      int
	queueDone     int

	// Manual-match file picker overlay, active only in matchStateDone.
	editingFile bool
	editingRef  trackRef
	editingRoot string
	editErr     string
	fp          filepicker.Model
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

	return MatchModel{cfg: cfg, spin: sp, list: l, prog: pg, tbl: tbl}
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
	m.list.SetSize(listW, m.contentHeight)

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
		if m.list.FilterState() == list.Filtering {
			return m.list
		}
		return matchListKeys
	case matchStateMatching, matchStateLoadingLibrary:
		return exportRunKeys
	case matchStateDone:
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
		items := make([]list.Item, len(msg.playlists))
		for i, p := range msg.playlists {
			items[i] = playlistItem{playlist: p}
		}
		m.list.SetItems(items)
		m.state = matchStateList
		return m, nil, matchActionNone

	case spinner.TickMsg:
		if m.state != matchStateAuthenticating && m.state != matchStateLoadingPlaylists && m.state != matchStateLoadingLibrary {
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
			idx := m.list.Index()
			if it, ok := m.list.SelectedItem().(playlistItem); ok {
				it.selected = !it.selected
				m.list.SetItem(idx, it)
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
			}
			return m, nil, matchActionNone
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
		if m.editingFile {
			return m.handleFilePickerKey(msg)
		}
		switch {
		case key.Matches(msg, matchDoneKeys.Back):
			return m, nil, matchActionBack
		case key.Matches(msg, matchDoneKeys.Edit):
			return m.beginEdit()
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
// missing.txt — no network, no cover re-download), and closes the overlay.
func (m MatchModel) applyManualMatch(path string) (MatchModel, tea.Cmd, matchAction) {
	m.editingFile = false
	m.editErr = ""

	run := m.runs[m.editingRef.runIdx]
	r := &run.results[m.editingRef.resultIdx]
	r.Method = match.MethodManual
	r.LocalPath = path
	r.Confidence = 1

	if res, err := m3u8.Rewrite(m.cfg.M3U8Dir, run.playlist, run.results); err != nil {
		m.editErr = "saving playlist: " + err.Error()
	} else {
		run.outcome.matched = res.Matched
		run.outcome.dir = res.Dir
	}

	m.tbl.SetRows(m.tableRows())
	return m, nil, matchActionNone
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
	var queue []spotifyapi.SimplifiedPlaylist
	for _, it := range m.list.Items() {
		p := it.(playlistItem)
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
	m.trackRefs = nil
	m.currentStatus = ""
	m.queueLen = len(queue)
	m.queueDone = 0
	m.state = matchStateMatching
	m.tbl.SetColumns(matchTableColumns(m.width))
	m.tbl.SetRows(nil)

	ch := make(chan matchEvent)
	m.matchEvents = ch
	go runMatch(m.ctx, m.client, m.httpClient, m.library, m.cfg, queue, ch)

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
			m.trackRefs = append(m.trackRefs, trackRef{runIdx: runIdx, resultIdx: i})
		}
		m.tbl.SetRows(m.tableRows())
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
		if m.editingFile {
			return m.viewFilePicker()
		}
		isrcN, fuzzyN, manualN, missN := 0, 0, 0, 0
		for _, ref := range m.trackRefs {
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
		summary := fmt.Sprintf("Done — %s  %s  %s  %s",
			badgeDone.Render(fmt.Sprintf("%d isrc", isrcN)),
			warnStyle.Render(fmt.Sprintf("%d fuzzy", fuzzyN)),
			accentStyle.Render(fmt.Sprintf("%d manual", manualN)),
			badgeFailed.Render(fmt.Sprintf("%d missing", missN)))
		return m.viewDone(summary)

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
	b.WriteString(m.tbl.View())
	return b.String()
}

// viewDone renders the finished results table alongside a detail panel for
// the currently selected track (full artist/album/path — the table itself
// only has room for a trimmed filename) and, below both, each playlist's
// write outcome.
func (m MatchModel) viewDone(heading string) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(heading) + "\n\n")

	tableW := (m.width * 3) / 5
	detailW := m.width - tableW - 3
	if detailW < 28 || m.width < 90 {
		m.tbl.SetWidth(m.width)
		m.tbl.SetColumns(matchTableColumns(m.width))
		m.tbl.SetRows(m.tableRows())
		b.WriteString(m.tbl.View())
		b.WriteString("\n")
		b.WriteString(m.renderTrackDetail(m.width - 4))
	} else {
		m.tbl.SetWidth(tableW)
		m.tbl.SetColumns(matchTableColumns(tableW))
		m.tbl.SetRows(m.tableRows())
		detail := panelStyle.Width(detailW).Height(m.contentHeight - 2).Render(m.renderTrackDetail(detailW - 2))
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, m.tbl.View(), "  ", detail))
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

	b.WriteString("\n" + fadedStyle.Render("enter to fix this match"))
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
		b.WriteString(dimStyle.Render(fmt.Sprintf("%s  ·  %s", playlistName, track)) + "\n")
	}
	b.WriteString(fadedStyle.Render(m.editingRoot) + "\n\n")

	if m.editErr != "" {
		b.WriteString(errorStyle.Render(m.editErr) + "\n\n")
	}

	b.WriteString(m.fp.View())
	return b.String()
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
	listW := (m.width * 3) / 5
	if listW < 30 {
		return m.list.View()
	}
	detailW := m.width - listW - 6
	if detailW < 20 {
		return m.list.View()
	}

	detail := panelStyle.Width(detailW).Height(m.contentHeight - 2).Render(m.renderDetail(detailW - 2))
	return lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), "  ", detail)
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

	if p.Description != "" {
		b.WriteString("\n" + dimStyle.Render("Description") + "\n")
		b.WriteString(bodyStyle.Width(width).Render(p.Description) + "\n")
	}

	b.WriteString("\n")
	if it.selected {
		b.WriteString(successStyle.Render("✓ selected for matching"))
	} else {
		b.WriteString(fadedStyle.Render("space to select for batch matching"))
	}

	return b.String()
}
