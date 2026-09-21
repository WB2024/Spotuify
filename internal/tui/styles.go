package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette. Colors are adaptive (light/dark terminal aware) where it
// matters for legibility; the Spotify green stays constant as the brand
// accent since it reads fine on either background.
var (
	spotifyGreen = lipgloss.Color("#1DB954")

	accent = lipgloss.Color("#B685FF") // used for detail-pane highlights, cursor accents

	fg     = lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#e4e4e4"}
	muted  = lipgloss.AdaptiveColor{Light: "#6b6b6b", Dark: "#8a8a8a"}
	faint  = lipgloss.AdaptiveColor{Light: "#9c9c9c", Dark: "#5c5c5c"}
	border = lipgloss.AdaptiveColor{Light: "#d0d0d0", Dark: "#3a3a3a"}

	errorRed  = lipgloss.AdaptiveColor{Light: "#b3261e", Dark: "#ff6b6b"}
	warnAmber = lipgloss.AdaptiveColor{Light: "#8a5a00", Dark: "#e0af5f"}
)

var (
	// Chrome
	appPad = lipgloss.NewStyle().Padding(1, 2)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(1, 2)

	panelTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(spotifyGreen)

	footerStyle = lipgloss.NewStyle().Foreground(faint)

	// Text
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("0")).
			Background(spotifyGreen).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(spotifyGreen)

	dimStyle     = lipgloss.NewStyle().Foreground(muted)
	fadedStyle   = lipgloss.NewStyle().Foreground(faint)
	bodyStyle    = lipgloss.NewStyle().Foreground(fg)
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(errorRed)
	warnStyle    = lipgloss.NewStyle().Foreground(warnAmber)
	successStyle = lipgloss.NewStyle().Foreground(spotifyGreen)
	accentStyle  = lipgloss.NewStyle().Foreground(accent)

	// Menu rows
	selectedRowStyle = lipgloss.NewStyle().Foreground(spotifyGreen).Bold(true)

	// Status badges (export summary)
	badgeDone   = lipgloss.NewStyle().Foreground(spotifyGreen)
	badgeFailed = lipgloss.NewStyle().Foreground(errorRed)
)

// gradientText renders s with each rune's foreground color smoothly
// interpolated between from and to (both "#rrggbb"), producing a
// left-to-right gradient without needing an image or external tool.
func gradientText(s string, from, to string, bold bool) string {
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return ""
	}
	fr, fg2, fb := hexToRGB(from)
	tr, tg, tb := hexToRGB(to)

	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		rr := lerp(fr, tr, t)
		gg := lerp(fg2, tg, t)
		bb := lerp(fb, tb, t)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", rr, gg, bb)))
		if bold {
			style = style.Bold(true)
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

func hexToRGB(hex string) (r, g, b int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return
}

func lerp(a, b int, t float64) int {
	v := float64(a) + (float64(b)-float64(a))*t
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return int(v)
}
