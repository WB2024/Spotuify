package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"spotuify/internal/config"
	"spotuify/internal/library"
	"spotuify/internal/lidarrapi"
	"spotuify/internal/lidarrmatch"
	"spotuify/internal/match"
)

// The Lidarr overlay sits on top of the match results screen: "l" resolves
// the selected track's album and lets the user pick which Lidarr album to
// add (with the add-only / add+search mode overridable per action), "L"
// does the same for every missing track in the batch automatically.

type lidarrOverlayState int

const (
	lidarrOff         lidarrOverlayState = iota
	lidarrLoading                        // resolving one track's candidates
	lidarrPicking                        // candidate list shown
	lidarrApplying                       // add/monitor/search in flight
	lidarrBulkConfirm                    // "add N albums for M missing tracks?"
	lidarrBulkRunning                    // bulk in progress
	lidarrResult                         // outcome / report shown
)

type lidarrOverlay struct {
	state  lidarrOverlayState
	target trackRef
	track  lidarrmatch.Track
	cands  []lidarrmatch.Candidate
	cursor int
	mode   lidarrmatch.Mode
	title  string
	err    string
	status string
	report []string

	bulkRefs []trackRef
	events   chan lidarrBulkEvent
	cancel   context.CancelFunc
}

func (o lidarrOverlay) active() bool { return o.state != lidarrOff }

func (o lidarrOverlay) busy() bool {
	return o.state == lidarrLoading || o.state == lidarrApplying || o.state == lidarrBulkRunning
}

type lidarrCandidatesMsg struct {
	target trackRef
	cands  []lidarrmatch.Candidate
	err    error
}

type lidarrAppliedMsg struct {
	target  trackRef
	summary string
	err     error
}

type lidarrBulkEvent struct {
	status string
	line   string // a finished report line, if any
	note   map[trackRef]string
	done   bool
}

func lidarrAddOptions(cfg *config.Config) lidarrapi.AddOptions {
	return lidarrapi.AddOptions{
		RootFolderPath:    cfg.LidarrRootFolder,
		QualityProfileID:  cfg.LidarrQualityProfileID,
		MetadataProfileID: cfg.LidarrMetadataProfileID,
	}
}

func defaultLidarrMode(cfg *config.Config) lidarrmatch.Mode {
	if cfg.LidarrAddAndSearch {
		return lidarrmatch.AddAndSearch
	}
	return lidarrmatch.AddOnly
}

func lookupLidarrCandidates(ctx context.Context, cfg *config.Config, target trackRef, track lidarrmatch.Track) tea.Cmd {
	return func() tea.Msg {
		mb := library.NewMusicBrainzResolver(cfg.LibraryCachePath)
		defer mb.Close()
		r := &lidarrmatch.Resolver{Lidarr: lidarrapi.New(cfg.LidarrURL, cfg.LidarrAPIKey), MusicBrainz: mb}
		cands, err := r.Candidates(ctx, track)
		return lidarrCandidatesMsg{target: target, cands: cands, err: err}
	}
}

func applyLidarrCandidate(ctx context.Context, cfg *config.Config, target trackRef, album lidarrapi.Album, mode lidarrmatch.Mode) tea.Cmd {
	return func() tea.Msg {
		client := lidarrapi.New(cfg.LidarrURL, cfg.LidarrAPIKey)
		summary, err := lidarrmatch.Apply(ctx, client, album, lidarrAddOptions(cfg), mode)
		return lidarrAppliedMsg{target: target, summary: summary, err: err}
	}
}

// bulkItem is one album's worth of missing tracks: several tracks from the
// same album collapse into one Lidarr add.
type bulkItem struct {
	track lidarrmatch.Track
	refs  []trackRef
}

// runLidarrBulk resolves and adds every item in turn, reporting over ch.
// One album at a time, deliberately: MusicBrainz is rate-limited anyway,
// and a batch is far easier to follow (and cancel) as a running list than
// as a burst.
func runLidarrBulk(ctx context.Context, cfg *config.Config, items []bulkItem, mode lidarrmatch.Mode, ch chan<- lidarrBulkEvent) {
	defer close(ch)

	send := func(ev lidarrBulkEvent) {
		select {
		case ch <- ev:
		case <-ctx.Done():
		}
	}

	mb := library.NewMusicBrainzResolver(cfg.LibraryCachePath)
	defer mb.Close()
	client := lidarrapi.New(cfg.LidarrURL, cfg.LidarrAPIKey)
	r := &lidarrmatch.Resolver{Lidarr: client, MusicBrainz: mb}
	opts := lidarrAddOptions(cfg)

	for i, it := range items {
		if ctx.Err() != nil {
			return
		}
		label := it.track.Artist + " – " + it.track.Album
		send(lidarrBulkEvent{status: fmt.Sprintf("%d/%d  %s", i+1, len(items), label)})

		note := map[trackRef]string{}
		var line string
		cands, err := r.Candidates(ctx, it.track)
		switch {
		case err != nil:
			line = errorStyle.Render("✗ ") + label + dimStyle.Render(": "+err.Error())
			for _, ref := range it.refs {
				note[ref] = "lookup failed: " + err.Error()
			}
		case len(cands) == 0:
			line = warnStyle.Render("? ") + label + dimStyle.Render(": no match in Lidarr's catalogue")
			for _, ref := range it.refs {
				note[ref] = "no match found"
			}
		case !lidarrmatch.Confident(cands[0]):
			// A weak text-search hit is usually the artist's *other* album
			// (typical for tracks off compilations) — not something to add
			// unasked. Left for the user to pick by hand with "l".
			line = warnStyle.Render("? ") + label + dimStyle.Render(fmt.Sprintf(": best guess only %.0f%% (%s) — pick by hand with l", cands[0].Score*100, cands[0].Album.Title))
			for _, ref := range it.refs {
				note[ref] = "ambiguous — pick by hand with l"
			}
		default:
			best := cands[0]
			summary, err := lidarrmatch.Apply(ctx, client, best.Album, opts, mode)
			picked := best.Album.Artist.ArtistName + " – " + best.Album.Title
			if err != nil {
				line = errorStyle.Render("✗ ") + picked + dimStyle.Render(": "+err.Error())
				for _, ref := range it.refs {
					note[ref] = "failed: " + err.Error()
				}
			} else {
				line = successStyle.Render("✓ ") + picked + dimStyle.Render(": "+summary)
				for _, ref := range it.refs {
					note[ref] = summary + " (" + best.Album.Title + ")"
				}
			}
		}
		send(lidarrBulkEvent{line: line, note: note})
	}
	send(lidarrBulkEvent{done: true})
}

func waitForLidarrBulkEvent(ch <-chan lidarrBulkEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return lidarrBulkEvent{done: true}
		}
		return ev
	}
}

// beginLidarr opens the overlay for the track under the results cursor.
func (m MatchModel) beginLidarr() (MatchModel, tea.Cmd, matchAction) {
	if err := m.cfg.ValidateLidarr(); err != nil {
		m.editErr = err.Error()
		return m, nil, matchActionNone
	}
	cursor := m.tbl.Cursor()
	_, r, ok := m.resultAt(cursor)
	if !ok || r.Item.Track == nil {
		return m, nil, matchActionNone
	}

	ctx, cancel := context.WithCancel(m.ctx)
	track := lidarrmatch.TrackFromSpotify(r.Item.Track)
	m.lidarr = lidarrOverlay{
		state:  lidarrLoading,
		target: m.trackRefs[cursor],
		track:  track,
		mode:   defaultLidarrMode(m.cfg),
		title:  track.Artist + " – " + track.Title,
		cancel: cancel,
	}
	m.editErr = ""
	return m, tea.Batch(m.spin.Tick, lookupLidarrCandidates(ctx, m.cfg, m.lidarr.target, track)), matchActionNone
}

// beginLidarrBulk opens the confirmation for adding every missing track's
// album across the whole batch.
func (m MatchModel) beginLidarrBulk() (MatchModel, tea.Cmd, matchAction) {
	if err := m.cfg.ValidateLidarr(); err != nil {
		m.editErr = err.Error()
		return m, nil, matchActionNone
	}
	var refs []trackRef
	for _, ref := range m.trackRefs {
		r := m.runs[ref.runIdx].results[ref.resultIdx]
		if r.Method == match.MethodNone && r.Item.Track != nil {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		m.editErr = "nothing is missing — every track matched"
		return m, nil, matchActionNone
	}
	m.lidarr = lidarrOverlay{
		state:    lidarrBulkConfirm,
		mode:     defaultLidarrMode(m.cfg),
		title:    "Add all missing to Lidarr",
		bulkRefs: refs,
	}
	m.editErr = ""
	return m, nil, matchActionNone
}

// bulkItems groups the missing tracks by album so each album is looked up
// and added once, whatever number of its tracks the playlist has.
func (m MatchModel) bulkItems() []bulkItem {
	byKey := map[string]*bulkItem{}
	var order []string
	for _, ref := range m.lidarr.bulkRefs {
		t := lidarrmatch.TrackFromSpotify(m.runs[ref.runIdx].results[ref.resultIdx].Item.Track)
		k := strings.ToLower(t.Artist + "\x00" + t.Album)
		it, ok := byKey[k]
		if !ok {
			it = &bulkItem{track: t}
			byKey[k] = it
			order = append(order, k)
		}
		it.refs = append(it.refs, ref)
	}
	items := make([]bulkItem, 0, len(order))
	for _, k := range order {
		items = append(items, *byKey[k])
	}
	return items
}

func (m MatchModel) closeLidarr() MatchModel {
	if m.lidarr.cancel != nil {
		m.lidarr.cancel()
	}
	m.lidarr = lidarrOverlay{}
	return m
}

func (m MatchModel) handleLidarrKey(msg tea.KeyMsg) (MatchModel, tea.Cmd, matchAction) {
	o := &m.lidarr

	if key.Matches(msg, lidarrKeys.Cancel) {
		return m.closeLidarr(), nil, matchActionNone
	}

	switch o.state {
	case lidarrPicking:
		switch {
		case key.Matches(msg, lidarrKeys.Up):
			if o.cursor > 0 {
				o.cursor--
			}
		case key.Matches(msg, lidarrKeys.Down):
			if o.cursor < len(o.cands)-1 {
				o.cursor++
			}
		case key.Matches(msg, lidarrKeys.ToggleMode):
			o.mode = toggleMode(o.mode)
		case key.Matches(msg, lidarrKeys.Confirm):
			if len(o.cands) == 0 {
				return m.closeLidarr(), nil, matchActionNone
			}
			ctx, cancel := context.WithCancel(m.ctx)
			o.cancel = cancel
			o.state = lidarrApplying
			return m, tea.Batch(m.spin.Tick, applyLidarrCandidate(ctx, m.cfg, o.target, o.cands[o.cursor].Album, o.mode)), matchActionNone
		}

	case lidarrBulkConfirm:
		switch {
		case key.Matches(msg, lidarrKeys.ToggleMode):
			o.mode = toggleMode(o.mode)
		case key.Matches(msg, lidarrKeys.Confirm):
			ctx, cancel := context.WithCancel(m.ctx)
			ch := make(chan lidarrBulkEvent)
			o.cancel = cancel
			o.events = ch
			o.state = lidarrBulkRunning
			go runLidarrBulk(ctx, m.cfg, m.bulkItems(), o.mode, ch)
			return m, tea.Batch(m.spin.Tick, waitForLidarrBulkEvent(ch)), matchActionNone
		}

	case lidarrResult:
		if key.Matches(msg, lidarrDoneKeys.Continue) {
			return m.closeLidarr(), nil, matchActionNone
		}
	}
	return m, nil, matchActionNone
}

func toggleMode(mode lidarrmatch.Mode) lidarrmatch.Mode {
	if mode == lidarrmatch.AddOnly {
		return lidarrmatch.AddAndSearch
	}
	return lidarrmatch.AddOnly
}

func (m MatchModel) handleLidarrMsg(msg tea.Msg) (MatchModel, tea.Cmd, bool) {
	o := &m.lidarr
	switch msg := msg.(type) {
	case lidarrCandidatesMsg:
		if o.state != lidarrLoading || msg.target != o.target {
			return m, nil, true // stale: overlay was closed or retargeted
		}
		if msg.err != nil {
			o.state = lidarrResult
			o.err = msg.err.Error()
			return m, nil, true
		}
		o.cands = msg.cands
		o.cursor = 0
		o.state = lidarrPicking
		return m, nil, true

	case lidarrAppliedMsg:
		if o.state != lidarrApplying || msg.target != o.target {
			return m, nil, true
		}
		o.state = lidarrResult
		if msg.err != nil {
			o.err = msg.err.Error()
			return m, nil, true
		}
		o.report = []string{successStyle.Render("✓ ") + o.cands[o.cursor].Album.Artist.ArtistName + " – " + o.cands[o.cursor].Album.Title + dimStyle.Render(": "+msg.summary)}
		m.setLidarrNote(o.target, msg.summary+" ("+o.cands[o.cursor].Album.Title+")")
		return m, nil, true

	case lidarrBulkEvent:
		if o.state != lidarrBulkRunning {
			return m, nil, true
		}
		if msg.status != "" {
			o.status = msg.status
		}
		if msg.line != "" {
			o.report = append(o.report, msg.line)
		}
		for ref, note := range msg.note {
			m.setLidarrNote(ref, note)
		}
		if msg.done {
			o.state = lidarrResult
			o.status = ""
			return m, nil, true
		}
		return m, waitForLidarrBulkEvent(o.events), true
	}
	return m, nil, false
}

func (m *MatchModel) setLidarrNote(ref trackRef, note string) {
	if m.lidarrNotes == nil {
		m.lidarrNotes = map[trackRef]string{}
	}
	m.lidarrNotes[ref] = note
}

func (m MatchModel) lidarrHelpKeys() help.KeyMap {
	switch m.lidarr.state {
	case lidarrResult:
		return lidarrDoneKeys
	case lidarrLoading, lidarrApplying, lidarrBulkRunning:
		return exportRunKeys
	default:
		return lidarrKeys
	}
}

func (m MatchModel) viewLidarr() string {
	o := m.lidarr
	var b strings.Builder
	b.WriteString(headerStyle.Render("Add to Lidarr") + "\n\n")
	if o.title != "" {
		b.WriteString(bodyStyle.Render(o.title) + "\n")
	}
	if o.track.Album != "" {
		b.WriteString(dimStyle.Render("Spotify album: "+o.track.Album) + "\n")
	}
	b.WriteString("\n")

	modeLine := func() string {
		return dimStyle.Render("Mode: ") + accentStyle.Render(o.mode.String()) + fadedStyle.Render("   (s to toggle; default from Settings)")
	}

	switch o.state {
	case lidarrLoading:
		b.WriteString(fmt.Sprintf("%s Looking up the album via MusicBrainz and Lidarr...\n", m.spin.View()))

	case lidarrPicking:
		if len(o.cands) == 0 {
			b.WriteString(warnStyle.Render("Nothing plausible found in Lidarr's catalogue for this track.") + "\n")
			b.WriteString(fadedStyle.Render("Try adding it by hand in Lidarr — esc to close") + "\n")
			break
		}
		b.WriteString(modeLine() + "\n\n")
		b.WriteString(dimStyle.Render("Which album?") + "\n")
		for i, c := range o.cands {
			b.WriteString(renderLidarrCandidate(c, i == o.cursor) + "\n")
		}

	case lidarrApplying:
		b.WriteString(fmt.Sprintf("%s %s...\n", m.spin.View(), o.mode))

	case lidarrBulkConfirm:
		items := m.bulkItems()
		b.WriteString(bodyStyle.Render(fmt.Sprintf("%d missing tracks across %d albums.", len(o.bulkRefs), len(items))) + "\n")
		b.WriteString(fadedStyle.Render("Each album's best match is taken automatically; anything ambiguous is reported, not guessed.") + "\n\n")
		b.WriteString(modeLine() + "\n\n")
		for _, it := range items {
			b.WriteString("  " + dimStyle.Render("• ") + bodyStyle.Render(it.track.Artist+" – "+it.track.Album) + "\n")
		}
		b.WriteString("\n" + fadedStyle.Render("enter to go · esc to cancel") + "\n")

	case lidarrBulkRunning:
		b.WriteString(fmt.Sprintf("%s %s\n\n", m.spin.View(), dimStyle.Render(o.status)))
		for _, line := range o.report {
			b.WriteString("  " + line + "\n")
		}

	case lidarrResult:
		if o.err != "" {
			b.WriteString(errorStyle.Render("✗ "+o.err) + "\n")
		}
		for _, line := range o.report {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n" + fadedStyle.Render("enter to close") + "\n")
	}
	return b.String()
}

func renderLidarrCandidate(c lidarrmatch.Candidate, selected bool) string {
	a := c.Album
	marker := "  "
	nameStyle := bodyStyle
	if selected {
		marker = accentStyle.Render("▸ ")
		nameStyle = selectedRowStyle
	}

	meta := a.AlbumType
	if y := a.Year(); y != "" {
		meta += " " + y
	}
	if a.Disambiguation != "" {
		meta += ", " + a.Disambiguation
	}

	var state string
	switch {
	case !a.InLidarr():
		state = accentStyle.Render("not in Lidarr yet")
	case a.Monitored:
		state = successStyle.Render("in Lidarr, monitored")
	default:
		state = warnStyle.Render("in Lidarr, unmonitored")
	}

	via := fadedStyle.Render("exact MusicBrainz match")
	if c.Via == "search" {
		via = fadedStyle.Render(fmt.Sprintf("text search, %.0f%%", c.Score*100))
	}

	return marker + nameStyle.Render(a.Artist.ArtistName+" – "+a.Title) + dimStyle.Render("  ("+meta+")") + "  " + state + "  " + via
}
