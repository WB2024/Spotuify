package tui

import (
	"context"
	"fmt"
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
	"spotuify/internal/export"
	"spotuify/internal/spotifyapi"
)

type exportScreenState int

const (
	exportStateAuthenticating exportScreenState = iota
	exportStateLoading
	exportStateList
	exportStateExporting
	exportStateDone
	exportStateFatal
)

// exportAction tells the root model what, if anything, it needs to do in
// response to something the export screen can't handle on its own.
type exportAction int

const (
	exportActionNone exportAction = iota
	exportActionBack
)

// exportStatusFilter narrows the playlist list to those that do/don't
// already have a local export (playlist.json) — cycled with "m" — so
// catching up on whatever's missing (e.g. after a batch run was cut short
// by a Spotify rate limit) is a keypress away instead of eyeballing the
// whole list or re-exporting everything from scratch.
type exportStatusFilter int

const (
	exportFilterAll exportStatusFilter = iota
	exportFilterMissing
	exportFilterExported
)

func (f exportStatusFilter) next() exportStatusFilter {
	return (f + 1) % 3
}

// playlistItem adapts a Spotify playlist to bubbles/list's Item interface,
// tracking whether the user has checked it for a batch export/match.
type playlistItem struct {
	playlist spotifyapi.SimplifiedPlaylist
	selected bool

	// hasFile means "already has the on-disk artifact this screen cares
	// about" — a written .m3u8 on the Match screen, a local export
	// (playlist.json) on this one. group is Match-screen-only: the
	// playlist's resolved Navidrome group (override if set, else the
	// global default). Export never sets it, so it's always zero-valued
	// here and never shown.
	hasFile bool
	group   string
}

func (i playlistItem) Title() string {
	box := "○"
	if i.selected {
		box = "◉"
	}
	title := fmt.Sprintf("%s %s", box, i.playlist.Name)
	if i.hasFile {
		title += "  " + successStyle.Render("✓ synced")
	}
	return title
}

func (i playlistItem) Description() string {
	owner := i.playlist.Owner.DisplayName
	if owner == "" {
		owner = i.playlist.Owner.ID
	}
	kind := "playlist"
	if i.playlist.Collaborative {
		kind = "collaborative playlist"
	}
	return fmt.Sprintf("%d tracks • %s by %s", i.playlist.Tracks.Total, kind, owner)
}

func (i playlistItem) FilterValue() string { return i.playlist.Name }

type exportRowStatus int

const (
	rowQueued exportRowStatus = iota
	rowRunning
	rowDone
	rowFailed
)

type exportRow struct {
	playlist spotifyapi.SimplifiedPlaylist
	status   exportRowStatus
	note     string
	tracks   int
	dir      string
}

func (r exportRow) statusLabel() string {
	switch r.status {
	case rowRunning:
		return "▸ running"
	case rowDone:
		return "✓ done"
	case rowFailed:
		return "✗ failed"
	default:
		return "queued"
	}
}

// ExportModel is the "Export Playlists" screen: it owns the (possibly
// interactive) login, the playlist browser, and the batch export run with
// its live status table.
type ExportModel struct {
	ctx    context.Context
	cfg    *config.Config
	client *spotifyapi.Client

	state exportScreenState

	spin spinner.Model
	list list.Model
	prog progress.Model
	tbl  table.Model

	width, height int

	user *spotifyapi.User
	err  error

	// allPlaylists is every loaded playlist, independent of what
	// statusFilter currently narrows e.list down to — selections and
	// hasFile are tracked here so they survive switching filters, since
	// e.list only ever holds whatever subset is currently visible.
	allPlaylists []playlistItem
	statusFilter exportStatusFilter

	rows     []exportRow
	events   chan exportEvent
	queueLen int
	done     int
}

func newExportModel(cfg *config.Config) ExportModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(spotifyGreen)

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(spotifyGreen).BorderLeftForeground(spotifyGreen)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(spotifyGreen).BorderLeftForeground(spotifyGreen)

	l := list.New(nil, delegate, 0, 0)
	l.Title = "Your Spotify Playlists"
	l.Styles.Title = titleStyle
	l.SetShowStatusBar(true)
	l.SetShowHelp(false) // help is rendered once, in the shared chrome footer
	l.SetFilteringEnabled(true)

	pg := progress.New(progress.WithGradient("#1ED760", "#1DB954"))

	tbl := table.New(table.WithFocused(true))
	tbl.SetStyles(tableStyles())

	return ExportModel{
		cfg:  cfg,
		spin: sp,
		list: l,
		prog: pg,
		tbl:  tbl,
	}
}

func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Foreground(spotifyGreen).Bold(true).BorderForeground(border).BorderBottom(true)
	s.Selected = s.Selected.Foreground(lipgloss.Color("0")).Background(spotifyGreen).Bold(true)
	s.Cell = s.Cell.Foreground(fg)
	return s
}

// SetSize propagates a terminal resize to every widget this screen owns.
func (e *ExportModel) SetSize(width, height int) {
	e.width, e.height = width, height
	contentHeight := height - 6
	if contentHeight < 5 {
		contentHeight = 5
	}

	listW := (width * 3) / 5
	if listW < 30 {
		listW = width
	}
	// -2: the status-filter chip row viewList draws above the list, plus
	// its blank-line separator.
	listHeight := contentHeight - 2
	if listHeight < 3 {
		listHeight = 3
	}
	e.list.SetSize(listW, listHeight)

	e.prog.Width = width - 4
	if e.prog.Width < 10 {
		e.prog.Width = 10
	}

	e.tbl.SetWidth(width)
	e.tbl.SetHeight(contentHeight)
	e.tbl.SetColumns(tableColumns(width))
}

func tableColumns(width int) []table.Column {
	numW, statusW, tracksW := 4, 10, 8
	rest := width - numW - statusW - tracksW - 12
	if rest < 24 {
		rest = 24
	}
	nameW := rest * 55 / 100
	noteW := rest - nameW
	return []table.Column{
		{Title: "#", Width: numW},
		{Title: "Playlist", Width: nameW},
		{Title: "Status", Width: statusW},
		{Title: "Tracks", Width: tracksW},
		{Title: "Note", Width: noteW},
	}
}

// Enter (re)starts this screen: authenticating first if there's no cached
// client yet, otherwise going straight to loading the playlist list.
func (e ExportModel) Enter(ctx context.Context) (ExportModel, tea.Cmd) {
	e.ctx = ctx
	e.err = nil

	if e.client != nil {
		e.state = exportStateLoading
		return e, tea.Batch(e.spin.Tick, loadPlaylists(ctx, e.client))
	}
	e.state = exportStateAuthenticating
	return e, tea.Batch(e.spin.Tick, authenticate(ctx, e.cfg))
}

// InvalidateClient discards any cached authenticated client, e.g. after the
// user changes credentials or logs out in Settings.
func (e *ExportModel) InvalidateClient() { e.client = nil }

func loadPlaylists(ctx context.Context, client *spotifyapi.Client) tea.Cmd {
	return func() tea.Msg {
		user, err := client.CurrentUser(ctx)
		if err != nil {
			return errMsg{err}
		}
		playlists, err := client.UserPlaylists(ctx)
		if err != nil {
			return errMsg{err}
		}
		return playlistsLoadedMsg{user: user, playlists: playlists}
	}
}

// Keys returns the keymap currently in effect, for the shared help bar.
func (e ExportModel) Keys() help.KeyMap {
	switch e.state {
	case exportStateList:
		if e.list.FilterState() == list.Filtering {
			return e.list // list.Model satisfies help.KeyMap directly
		}
		return playlistListKeys
	case exportStateExporting:
		return exportRunKeys
	case exportStateDone, exportStateFatal:
		return exportDoneKeys
	default:
		return exportRunKeys
	}
}

func (e ExportModel) Update(msg tea.Msg) (ExportModel, tea.Cmd, exportAction) {
	switch msg := msg.(type) {

	case authDoneMsg:
		if msg.err != nil {
			e.state = exportStateFatal
			e.err = msg.err
			return e, nil, exportActionNone
		}
		e.client = spotifyapi.New(msg.httpClient)
		e.state = exportStateLoading
		return e, tea.Batch(e.spin.Tick, loadPlaylists(e.ctx, e.client)), exportActionNone

	case errMsg:
		e.state = exportStateFatal
		e.err = msg.err
		return e, nil, exportActionNone

	case playlistsLoadedMsg:
		e.user = msg.user
		exportsByID := export.ScanExports(e.cfg.ExportDir)
		items := make([]playlistItem, len(msg.playlists))
		for i, p := range msg.playlists {
			_, hasExport := exportsByID[p.ID]
			items[i] = playlistItem{playlist: p, hasFile: hasExport}
		}
		e.allPlaylists = items
		e.statusFilter = exportFilterAll
		e.applyStatusFilter()
		e.state = exportStateList
		return e, nil, exportActionNone

	case spinner.TickMsg:
		if e.state != exportStateAuthenticating && e.state != exportStateLoading {
			return e, nil, exportActionNone
		}
		var cmd tea.Cmd
		e.spin, cmd = e.spin.Update(msg)
		return e, cmd, exportActionNone

	case progress.FrameMsg:
		newModel, cmd := e.prog.Update(msg)
		e.prog = newModel.(progress.Model)
		return e, cmd, exportActionNone

	case exportEvent:
		return e.handleExportEvent(msg)

	case tea.KeyMsg:
		return e.handleKey(msg)
	}

	switch e.state {
	case exportStateList:
		if d := wheelDelta(msg); d != 0 {
			scrollList(&e.list, d)
			return e, nil, exportActionNone
		}
		var cmd tea.Cmd
		e.list, cmd = e.list.Update(msg)
		return e, cmd, exportActionNone
	case exportStateExporting, exportStateDone:
		// bubbles/table (v1.0.0) has no mouse handling of its own, so the
		// wheel has to be translated into cursor moves by hand — forwarding
		// the raw tea.MouseMsg to tbl.Update does nothing.
		if d := wheelDelta(msg); d != 0 {
			scrollTable(&e.tbl, d)
			return e, nil, exportActionNone
		}
		var cmd tea.Cmd
		e.tbl, cmd = e.tbl.Update(msg)
		return e, cmd, exportActionNone
	}
	return e, nil, exportActionNone
}

// IsFiltering reports whether the playlist list's text filter currently has
// focus, so the root model knows to route every keystroke into it instead
// of treating them as shortcuts.
func (e ExportModel) IsFiltering() bool {
	return e.state == exportStateList && e.list.FilterState() == list.Filtering
}

func (e ExportModel) handleKey(msg tea.KeyMsg) (ExportModel, tea.Cmd, exportAction) {
	if e.state == exportStateList && e.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		e.list, cmd = e.list.Update(msg)
		return e, cmd, exportActionNone
	}

	switch e.state {
	case exportStateList:
		switch {
		case key.Matches(msg, playlistListKeys.Back):
			return e, nil, exportActionBack
		case key.Matches(msg, playlistListKeys.Toggle):
			idx := e.list.GlobalIndex() // SetItem indexes into the unfiltered list; Index() doesn't when a text filter is active
			if it, ok := e.list.SelectedItem().(playlistItem); ok {
				it.selected = !it.selected
				e.list.SetItem(idx, it)
				e.syncAllPlaylist(it)
			}
			return e, nil, exportActionNone
		case key.Matches(msg, playlistListKeys.SelectAll):
			items := e.list.Items()
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
				e.list.SetItem(i, p)
				e.syncAllPlaylist(p)
			}
			return e, nil, exportActionNone
		case key.Matches(msg, playlistListKeys.StatusFilter):
			e.statusFilter = e.statusFilter.next()
			e.applyStatusFilter()
			return e, nil, exportActionNone
		case key.Matches(msg, playlistListKeys.Export):
			return e.startExport()
		}
		var cmd tea.Cmd
		e.list, cmd = e.list.Update(msg)
		return e, cmd, exportActionNone

	case exportStateExporting:
		if key.Matches(msg, exportRunKeys.Cancel) {
			return e, nil, exportActionBack
		}
		var cmd tea.Cmd
		e.tbl, cmd = e.tbl.Update(msg)
		return e, cmd, exportActionNone

	case exportStateAuthenticating, exportStateLoading:
		if key.Matches(msg, exportRunKeys.Cancel) {
			return e, nil, exportActionBack
		}
		return e, nil, exportActionNone

	case exportStateDone, exportStateFatal:
		if key.Matches(msg, exportDoneKeys.Continue) {
			return e, nil, exportActionBack
		}
		var cmd tea.Cmd
		e.tbl, cmd = e.tbl.Update(msg)
		return e, cmd, exportActionNone
	}

	return e, nil, exportActionNone
}

// syncAllPlaylist writes it back into e.allPlaylists (matched by playlist
// ID), so a selection toggle made while a status filter narrows the
// visible list isn't lost when the filter changes again — e.list only ever
// holds a subset, e.allPlaylists is the durable source of truth.
func (e *ExportModel) syncAllPlaylist(it playlistItem) {
	for i := range e.allPlaylists {
		if e.allPlaylists[i].playlist.ID == it.playlist.ID {
			e.allPlaylists[i] = it
			return
		}
	}
}

// applyStatusFilter rebuilds e.list's visible items from e.allPlaylists
// according to e.statusFilter.
func (e *ExportModel) applyStatusFilter() {
	visible := make([]list.Item, 0, len(e.allPlaylists))
	for _, it := range e.allPlaylists {
		switch e.statusFilter {
		case exportFilterMissing:
			if it.hasFile {
				continue
			}
		case exportFilterExported:
			if !it.hasFile {
				continue
			}
		}
		visible = append(visible, it)
	}
	e.list.SetItems(visible)
}

// renderStatusChips shows how many loaded playlists already have a local
// export vs. don't, with the active statusFilter highlighted — both a
// quick summary and a reminder that "m" cycles it.
func (e ExportModel) renderStatusChips() string {
	var exported, missing int
	for _, p := range e.allPlaylists {
		if p.hasFile {
			exported++
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

	return chip("All", len(e.allPlaylists), e.statusFilter == exportFilterAll) + " " +
		chip("Exported", exported, e.statusFilter == exportFilterExported) + " " +
		chip("Missing", missing, e.statusFilter == exportFilterMissing) + "   " +
		fadedStyle.Render("m to cycle")
}

// startExport queues every checked playlist for export. Selections are
// checked against the full set (e.allPlaylists), not just e.list's current
// (possibly status-filtered) view — a playlist checked while filtered to
// "Missing" stays queued even after switching back to "All".
func (e ExportModel) startExport() (ExportModel, tea.Cmd, exportAction) {
	var queue []spotifyapi.SimplifiedPlaylist
	for _, p := range e.allPlaylists {
		if p.selected {
			queue = append(queue, p.playlist)
		}
	}
	if len(queue) == 0 {
		if cur, ok := e.list.SelectedItem().(playlistItem); ok {
			queue = []spotifyapi.SimplifiedPlaylist{cur.playlist}
		}
	}
	if len(queue) == 0 {
		return e, nil, exportActionNone
	}

	e.rows = make([]exportRow, len(queue))
	for i, p := range queue {
		e.rows[i] = exportRow{playlist: p, status: rowQueued}
	}
	e.queueLen = len(queue)
	e.done = 0
	e.state = exportStateExporting
	e.tbl.SetColumns(tableColumns(e.width))
	e.tbl.SetRows(rowsToTable(e.rows))
	e.tbl.SetCursor(0)

	exporter := export.New(e.cfg.ExportDir, e.cfg.DownloadCovers, nil)

	ch := make(chan exportEvent)
	e.events = ch
	go runExport(e.ctx, e.client, exporter, queue, ch)

	return e, tea.Batch(waitForExportEvent(ch), e.prog.SetPercent(0)), exportActionNone
}

func rowsToTable(rows []exportRow) []table.Row {
	out := make([]table.Row, len(rows))
	for i, r := range rows {
		tracks := "-"
		if r.tracks > 0 {
			tracks = fmt.Sprintf("%d", r.tracks)
		}
		out[i] = table.Row{fmt.Sprintf("%d", i+1), r.playlist.Name, r.statusLabel(), tracks, r.note}
	}
	return out
}

func (e ExportModel) handleExportEvent(ev exportEvent) (ExportModel, tea.Cmd, exportAction) {
	switch ev.kind {
	case eventStatus:
		if e.done < len(e.rows) {
			e.rows[e.done].status = rowRunning
			e.rows[e.done].note = ev.text
			e.tbl.SetRows(rowsToTable(e.rows))
			e.tbl.SetCursor(e.done)
		}
		return e, waitForExportEvent(e.events), exportActionNone

	case eventPlaylistDone:
		if e.done < len(e.rows) {
			if ev.err != nil {
				e.rows[e.done].status = rowFailed
				e.rows[e.done].note = ev.err.Error()
			} else {
				e.rows[e.done].status = rowDone
				e.rows[e.done].note = ev.dir
				e.rows[e.done].tracks = ev.trackCount
				e.rows[e.done].dir = ev.dir
			}
			e.tbl.SetRows(rowsToTable(e.rows))
		}
		e.done++
		e.tbl.SetCursor(min(e.done, len(e.rows)-1))
		pct := float64(e.done) / float64(e.queueLen)
		cmd := e.prog.SetPercent(pct)
		return e, tea.Batch(cmd, waitForExportEvent(e.events)), exportActionNone

	case eventAllDone:
		e.state = exportStateDone
		return e, nil, exportActionNone
	}
	return e, nil, exportActionNone
}

func (e ExportModel) View() string {
	switch e.state {
	case exportStateAuthenticating:
		return fmt.Sprintf("\n  %s Waiting for Spotify login in your browser...\n", e.spin.View())

	case exportStateLoading:
		return fmt.Sprintf("\n  %s Loading your Spotify playlists...\n", e.spin.View())

	case exportStateList:
		return e.viewList()

	case exportStateExporting:
		return e.viewBatch(fmt.Sprintf("Exporting %d/%d playlists...", e.done, e.queueLen))

	case exportStateDone:
		ok, failed := 0, 0
		for _, r := range e.rows {
			if r.status == rowDone {
				ok++
			} else if r.status == rowFailed {
				failed++
			}
		}
		summary := "Export complete — " + badgeDone.Render(fmt.Sprintf("%d succeeded", ok))
		if failed > 0 {
			summary += "  " + badgeFailed.Render(fmt.Sprintf("%d failed", failed))
		}
		return e.viewBatch(summary)

	case exportStateFatal:
		return errorStyle.Render("Error: "+e.err.Error()) + "\n"
	}
	return ""
}

func (e ExportModel) viewBatch(heading string) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(heading) + "\n\n")
	b.WriteString(e.prog.View())
	b.WriteString("\n\n")
	b.WriteString(e.tbl.View())
	return b.String()
}

func (e ExportModel) viewList() string {
	chips := e.renderStatusChips() + "\n\n"

	contentHeight := e.height - 6
	if contentHeight < 5 {
		contentHeight = 5
	}

	listW := (e.width * 3) / 5
	if listW < 30 {
		return chips + e.list.View()
	}
	detailW := e.width - listW - 6
	if detailW < 20 {
		return chips + e.list.View()
	}

	detail := panelStyle.Width(detailW).Height(contentHeight - 2).Render(e.renderDetail(detailW - 2))
	return chips + lipgloss.JoinHorizontal(lipgloss.Top, e.list.View(), "  ", detail)
}

func (e ExportModel) renderDetail(width int) string {
	it, ok := e.list.SelectedItem().(playlistItem)
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

	vis := "Unknown"
	if p.Public != nil {
		vis = "Private"
		if *p.Public {
			vis = "Public"
		}
	}
	if p.Collaborative {
		vis += " · Collaborative"
	}
	row("Visibility", vis)
	row("Tracks", accentStyle.Render(fmt.Sprintf("%d", p.Tracks.Total)))

	if p.Description != "" {
		b.WriteString("\n" + dimStyle.Render("Description") + "\n")
		b.WriteString(bodyStyle.Width(width).Render(p.Description) + "\n")
	}

	b.WriteString("\n")
	if it.selected {
		b.WriteString(successStyle.Render("✓ selected for export"))
	} else {
		b.WriteString(fadedStyle.Render("space to select for batch export"))
	}

	return b.String()
}
