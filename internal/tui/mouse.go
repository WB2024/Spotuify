package tui

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// wheelDelta translates a mouse-wheel event into a scroll direction: -1 for
// wheel-up, +1 for wheel-down, 0 for anything else (including every other
// mouse event bubbletea reports, since WithMouseCellMotion also delivers
// motion/click events this app has no use for). bubbles/table and
// bubbles/list (v1.0.0) don't handle tea.MouseMsg at all — Update on both is
// keyboard-only — so every screen that embeds one has to translate wheel
// events into their exported cursor-movement calls by hand for the mouse
// wheel to do anything.
func wheelDelta(msg tea.Msg) int {
	m, ok := msg.(tea.MouseMsg)
	if !ok {
		return 0
	}
	switch m.Button {
	case tea.MouseButtonWheelUp:
		return -1
	case tea.MouseButtonWheelDown:
		return 1
	}
	return 0
}

// scrollLines is how many rows one wheel notch moves — a bit more than a
// single keyboard press so scrolling a few hundred rows doesn't take
// forever, without overshooting what the user was aiming for.
const scrollLines = 3

// scrollList moves a bubbles/list's cursor scrollLines rows in the
// direction wheelDelta reports (negative: up, positive: down).
func scrollList(l *list.Model, delta int) {
	for i := 0; i < scrollLines; i++ {
		if delta < 0 {
			l.CursorUp()
		} else {
			l.CursorDown()
		}
	}
}

// scrollTable moves a bubbles/table's cursor scrollLines rows in the
// direction wheelDelta reports (negative: up, positive: down).
func scrollTable(t *table.Model, delta int) {
	if delta < 0 {
		t.MoveUp(scrollLines)
	} else {
		t.MoveDown(scrollLines)
	}
}
