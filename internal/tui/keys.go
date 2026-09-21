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
	Up, Down, Edit, Toggle, Back, Help key.Binding
}

func (k settingsNavKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Edit, k.Toggle, k.Back, k.Help}
}

func (k settingsNavKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Edit, k.Toggle}, {k.Back, k.Help}}
}

var settingsNavKeys = settingsNavKeyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Edit:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit/select")),
	Toggle: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
	Back:   key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "back")),
	Help:   globalKeys.Help,
}

// --- Settings: editing a field ---

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
