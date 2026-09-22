package tui

import "github.com/charmbracelet/bubbles/key"

// Every screen exposes its current keymap to the shared help bar via this
// interface (satisfied by bubbles/help.KeyMap). Centralizing keybindings
// here — rather than scattering raw string comparisons through Update
// methods — is what lets the help bar auto-render correctly as screens are
// added, and keeps every screen's controls declared in one obvious place.

type globalKeyMap struct {
	Quit key.Binding
	Help key.Binding
}

var globalKeys = globalKeyMap{
	Quit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
}

// --- Main menu ---

type menuKeyMap struct {
	Up, Down, Select, Quit, Help key.Binding
}

func (k menuKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Select, k.Quit, k.Help}
}

func (k menuKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Select}, {k.Quit, k.Help}}
}

var menuKeys = menuKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Quit:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "quit")),
	Help:   globalKeys.Help,
}

// --- Settings: navigating rows ---

type settingsNavKeyMap struct {
	Up, Down, Edit, Toggle, Save, Back, Help key.Binding
}

func (k settingsNavKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Edit, k.Toggle, k.Save, k.Back, k.Help}
}

func (k settingsNavKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Edit, k.Toggle}, {k.Save}, {k.Back, k.Help}}
}

var settingsNavKeys = settingsNavKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Edit:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit/select")),
	Toggle: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
	Save:   key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
	Back:   key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
	Help:   globalKeys.Help,
}

// --- Settings: editing a field ---
//
// Also reused as-is by the Match screen's inline "edit Navidrome group"
// field (match_screen.go) — that one has no Save concept, so it stays
// just Confirm; see settingsFieldEditKeys below for Settings' own
// editing-state keymap, which adds ctrl+s.

type settingsEditKeyMap struct {
	Confirm key.Binding
}

func (k settingsEditKeyMap) ShortHelp() []key.Binding { return []key.Binding{k.Confirm} }
func (k settingsEditKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Confirm}}
}

var settingsEditKeys = settingsEditKeyMap{
	Confirm: key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter/esc", "confirm field")),
}

// settingsFieldEditKeyMap is Settings' own text-field-editing keymap — like
// settingsEditKeyMap, plus ctrl+s to confirm the field and save the whole
// form in one step.
type settingsFieldEditKeyMap struct {
	Confirm key.Binding
	Save    key.Binding
}

func (k settingsFieldEditKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Confirm, k.Save}
}
func (k settingsFieldEditKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Confirm}, {k.Save}}
}

var settingsFieldEditKeys = settingsFieldEditKeyMap{
	Confirm: settingsEditKeys.Confirm,
	Save:    settingsNavKeys.Save,
}

// --- Export: playlist list ---

type playlistListKeyMap struct {
	Up, Down, Toggle, SelectAll, Export, Filter, Back, Help key.Binding
}

func (k playlistListKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Toggle, k.SelectAll, k.Export, k.Filter, k.Back, k.Help}
}

func (k playlistListKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Toggle, k.SelectAll}, {k.Export, k.Filter}, {k.Back, k.Help}}
}

var playlistListKeys = playlistListKeyMap{
	Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Toggle:    key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "select")),
	SelectAll: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all")),
	Export:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "export")),
	Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	Back:      key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
	Help:      globalKeys.Help,
}

// --- Export: running / done / fatal ---

type exportRunKeyMap struct {
	Cancel key.Binding
}

func (k exportRunKeyMap) ShortHelp() []key.Binding  { return []key.Binding{k.Cancel} }
func (k exportRunKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{{k.Cancel}} }

var exportRunKeys = exportRunKeyMap{
	Cancel: key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "cancel & back")),
}

type exportDoneKeyMap struct {
	Continue key.Binding
}

func (k exportDoneKeyMap) ShortHelp() []key.Binding  { return []key.Binding{k.Continue} }
func (k exportDoneKeyMap) FullHelp() [][]key.Binding { return [][]key.Binding{{k.Continue}} }

var exportDoneKeys = exportDoneKeyMap{
	Continue: key.NewBinding(key.WithKeys("enter", "esc", "q"), key.WithHelp("enter", "back to menu")),
}

// --- Match: playlist list (same shape as playlistListKeyMap, "match"
// instead of "export" in the help text) ---

type matchListKeyMap struct {
	Up, Down, Toggle, SelectAll, Match, Filter, StatusFilter, EditGroup, Back, Help key.Binding
}

func (k matchListKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Toggle, k.SelectAll, k.Match, k.Filter, k.StatusFilter, k.EditGroup, k.Back, k.Help}
}

func (k matchListKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Toggle, k.SelectAll}, {k.Match, k.Filter}, {k.StatusFilter, k.EditGroup}, {k.Back, k.Help}}
}

var matchListKeys = matchListKeyMap{
	Up:           key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:         key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Toggle:       key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "select")),
	SelectAll:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all")),
	Match:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "match & write m3u8")),
	Filter:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter by name")),
	StatusFilter: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "cycle has file/missing")),
	EditGroup:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "edit Navidrome group")),
	Back:         key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
	Help:         globalKeys.Help,
}

// --- Match: results table (Done screen) — "enter" opens the file picker to
// correct the selected row's match instead of leaving the screen, so it
// can't share exportDoneKeyMap's "enter also means back" binding. ---

type matchDoneKeyMap struct {
	Up, Down, Edit, Lidarr, LidarrAll                               key.Binding
	FilterISRC, FilterFuzzy, FilterManual, FilterMissing, FilterAll key.Binding
	FilterHelp                                                      key.Binding // display-only: 1-4/0 combined, for the help bar
	Back, Help                                                      key.Binding
}

func (k matchDoneKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Edit, k.FilterHelp, k.Lidarr, k.LidarrAll, k.Back, k.Help}
}

func (k matchDoneKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Edit}, {k.FilterHelp}, {k.Lidarr, k.LidarrAll}, {k.Back, k.Help}}
}

var matchDoneKeys = matchDoneKeyMap{
	Up:            key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:          key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Edit:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "fix match")),
	Lidarr:        key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "add album to Lidarr")),
	LidarrAll:     key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "add all missing to Lidarr")),
	FilterISRC:    key.NewBinding(key.WithKeys("1")),
	FilterFuzzy:   key.NewBinding(key.WithKeys("2")),
	FilterManual:  key.NewBinding(key.WithKeys("3")),
	FilterMissing: key.NewBinding(key.WithKeys("4")),
	FilterAll:     key.NewBinding(key.WithKeys("0")),
	FilterHelp:    key.NewBinding(key.WithKeys("1", "2", "3", "4", "0"), key.WithHelp("1-4/0", "toggle isrc/fuzzy/manual/missing, 0=all")),
	Back:          key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back to menu")),
	Help:          globalKeys.Help,
}

// --- Match: Lidarr overlay (picking an album / confirming a bulk add) ---

type lidarrKeyMap struct {
	Up, Down, ToggleMode, Confirm, Cancel, Help key.Binding
}

func (k lidarrKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Confirm, k.ToggleMode, k.Cancel, k.Help}
}

func (k lidarrKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Confirm, k.ToggleMode}, {k.Cancel, k.Help}}
}

var lidarrKeys = lidarrKeyMap{
	Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	ToggleMode: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "toggle add only / add + search")),
	Confirm:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
	Cancel:     key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "cancel")),
	Help:       globalKeys.Help,
}

// lidarrDoneKeys is the overlay's keymap once it's just showing a result.
var lidarrDoneKeys = exportDoneKeyMap{
	Continue: key.NewBinding(key.WithKeys("enter", "esc", "q"), key.WithHelp("enter", "close")),
}

// --- Match: file-picker overlay (correcting one track's match) ---

type matchEditKeyMap struct {
	Up, Down, Select, Cancel, Help key.Binding
}

func (k matchEditKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Select, k.Cancel, k.Help}
}

func (k matchEditKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Select}, {k.Cancel, k.Help}}
}

var matchEditKeys = matchEditKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Select: key.NewBinding(key.WithKeys("enter", "l", "right"), key.WithHelp("enter", "open/pick")),
	Cancel: key.NewBinding(key.WithKeys("esc", "h", "left", "backspace"), key.WithHelp("esc", "back/cancel")),
	Help:   globalKeys.Help,
}
