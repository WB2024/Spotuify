package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type menuItem int

const (
	menuExport menuItem = iota
	menuMatch
	menuSettings
	menuItemCount
)

func (i menuItem) icon() string {
	switch i {
	case menuExport:
		return "♪"
	case menuMatch:
		return "⇄"
	case menuSettings:
		return "⚙"
	default:
		return "•"
	}
}

func (i menuItem) label() string {
	switch i {
	case menuExport:
		return "Export Playlists"
	case menuMatch:
		return "Match to Local Library"
	case menuSettings:
		return "Settings"
	default:
		return ""
	}
}

func (i menuItem) description() string {
	switch i {
	case menuExport:
		return "Browse your Spotify playlists and export them to JSON, CSV, and cover art"
	case menuMatch:
		return "Map playlist tracks to your Navidrome library and write .m3u8 playlists"
	case menuSettings:
		return "Spotify credentials, export location, and other options"
	default:
		return ""
	}
}

var (
	menuItemBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 2).
			Width(56)

	menuItemBoxActive = menuItemBox.BorderForeground(spotifyGreen)
)

func renderMainMenu(cursor menuItem, message string) string {
	var b strings.Builder

	for i := menuItem(0); i < menuItemCount; i++ {
		active := i == cursor

		iconStyle := dimStyle
		labelStyle := bodyStyle.Bold(true)
		descStyle := dimStyle
		box := menuItemBox
		if active {
			iconStyle = successStyle
			labelStyle = successStyle.Bold(true)
			descStyle = accentStyle
			box = menuItemBoxActive
		}

		line := iconStyle.Render(i.icon()) + "  " + labelStyle.Render(i.label())
		desc := descStyle.Render(i.description())
		b.WriteString(box.Render(line+"\n"+desc) + "\n")
		if i != menuItemCount-1 {
			b.WriteString("\n")
		}
	}

	if message != "" {
		b.WriteString("\n" + errorStyle.Render("⚠ "+message) + "\n")
	}

	return b.String()
}
