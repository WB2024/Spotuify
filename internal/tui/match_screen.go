package tui

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

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

// matchRow is one track's result, flattened for the results table. Kept
// separate from match.Result so the table can hold rows spanning several
// playlists in one batch run.
type matchRow struct {
	playlist string
	track    string
	artist   string
	method   match.Method
	note     string // matched file's base name, or the error/empty for missing
}

func (r matchRow) methodLabel() string {
	switch r.method {
	case match.MethodISRC:
		return "✓ isrc"
	case match.MethodFuzzy:
		return "~ fuzzy"
	default:
		return "✗ missing"
	}
}

// playlistOutcome summarizes one playlist's match+write run, for the Done
// screen.
type playlistOutcome struct {
	name        string
	dir         string
	matched     int
	total       int
	err         error
	coverUpload string // non-empty: cover.jpg written but not uploaded to Navidrome, and why
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

	width, height int

	user *spotifyapi.User
	err  error

	libEvents chan libraryLoadEvent
	libPhase  string
	libDone   int
	libTotal  int

	matchEvents   chan matchEvent
	currentStatus string
	rows          []matchRow
	outcomes      []playlistOutcome
	queueLen      int
	queueDone     int
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

// SetSize propagates a terminal resize to every widget this screen owns.
func (m *MatchModel) SetSize(width, height int) {
	m.width, m.height = width, height
	contentHeight := height - 6
	if contentHeight < 5 {
		contentHeight = 5
	}

	listW := (width * 3) / 5
	if listW < 30 {
		listW = width
	}
	m.list.SetSize(listW, contentHeight)

	m.prog.Width = width - 4
	if m.prog.Width < 10 {
		m.prog.Width = 10
	}

	m.tbl.SetWidth(width)
	m.tbl.SetHeight(contentHeight)
	m.tbl.SetColumns(matchTableColumns(width))
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
	case matchStateDone, matchStateFatal:
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
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd, matchActionNone
	case matchStateMatching, matchStateDone:
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

	case matchStateDone, matchStateFatal:
		if key.Matches(msg, exportDoneKeys.Continue) {
			return m, nil, matchActionBack
		}
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd, matchActionNone
	}

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

	m.rows = nil
	m.outcomes = nil
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
		outcome := playlistOutcome{name: ev.playlistName, total: len(ev.results), coverUpload: ev.coverUploadWarn}
		if ev.err != nil {
			outcome.err = ev.err
		}
		if ev.write != nil {
			outcome.dir = ev.write.Dir
			outcome.matched = ev.write.Matched
		}
		m.outcomes = append(m.outcomes, outcome)

		for _, r := range ev.results {
			if r.Item.Track == nil {
				continue
			}
			note := ""
			switch {
			case r.Method != match.MethodNone && r.LocalPath != "":
				note = filepath.Base(r.LocalPath)
			case ev.err != nil:
				note = "error: " + ev.err.Error()
			}
			m.rows = append(m.rows, matchRow{
				playlist: ev.playlistName,
				track:    r.Item.Track.Name,
				artist:   artistList(r.Item.Track.Artists),
				method:   r.Method,
				note:     note,
			})
		}
		m.tbl.SetRows(rowsToMatchTable(m.rows))
		m.tbl.GotoBottom()

		pct := float64(m.queueDone) / float64(m.queueLen)
		cmd := m.prog.SetPercent(pct)
		return m, tea.Batch(cmd, waitForMatchEvent(m.matchEvents)), matchActionNone

	case matchEventAllDone:
		m.state = matchStateDone
		return m, nil, matchActionNone
	}
	return m, nil, matchActionNone
}

func rowsToMatchTable(rows []matchRow) []table.Row {
	out := make([]table.Row, len(rows))
	for i, r := range rows {
		track := r.track
		if r.artist != "" {
			track = r.artist + " – " + r.track
		}
		out[i] = table.Row{r.playlist, track, r.methodLabel(), r.note}
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
		isrcN, fuzzyN, missN := 0, 0, 0
		for _, r := range m.rows {
			switch r.method {
			case match.MethodISRC:
				isrcN++
			case match.MethodFuzzy:
				fuzzyN++
			default:
				missN++
			}
		}
		summary := fmt.Sprintf("Done — %s  %s  %s",
			badgeDone.Render(fmt.Sprintf("%d isrc", isrcN)),
			warnStyle.Render(fmt.Sprintf("%d fuzzy", fuzzyN)),
			badgeFailed.Render(fmt.Sprintf("%d missing", missN)))
		return m.viewBatch(summary) + "\n" + m.viewOutcomes()

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

func (m MatchModel) viewOutcomes() string {
	var b strings.Builder
	for _, o := range m.outcomes {
		if o.err != nil {
			b.WriteString(errorStyle.Render(fmt.Sprintf("✗ %s: %v", o.name, o.err)) + "\n")
			continue
		}
		b.WriteString(dimStyle.Render(fmt.Sprintf("%s: %d/%d matched → %s", o.name, o.matched, o.total, o.dir)) + "\n")
		if o.coverUpload != "" {
			b.WriteString(warnStyle.Render(fmt.Sprintf("    cover art: %s", o.coverUpload)) + "\n")
		}
	}
	return b.String()
}

func (m MatchModel) viewList() string {
	contentHeight := m.height - 6
	if contentHeight < 5 {
		contentHeight = 5
	}

	listW := (m.width * 3) / 5
	if listW < 30 {
		return m.list.View()
	}
	detailW := m.width - listW - 6
	if detailW < 20 {
		return m.list.View()
	}

	detail := panelStyle.Width(detailW).Height(contentHeight - 2).Render(m.renderDetail(detailW - 2))
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
