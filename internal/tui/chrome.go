package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
)

const appName = "SPOTUIFY"

// logo renders the app wordmark as a left-to-right green gradient. It's the
// one constant piece of chrome shown on every screen, so the app reads as a
// single cohesive product rather than a stack of separate views.
func logo() string {
	return gradientText(appName, "#1ED760", "#14833B", true)
}

// frame lays out the shared chrome around a screen's content: the gradient
// logo, an optional breadcrumb, the content itself, and a help bar driven
// by that screen's current keymap. Every screen renders through this, so
// adding a new screen later means writing its content function and a
// keymap — the chrome is already handled.
func frame(width, height int, breadcrumb string, content string, help string) string {
	var top strings.Builder
	top.WriteString(logo())
	if breadcrumb != "" {
		top.WriteString("  ")
		top.WriteString(fadedStyle.Render("›"))
		top.WriteString(" ")
		top.WriteString(headerStyle.Render(breadcrumb))
	}

	var b strings.Builder
	b.WriteString(top.String())
	b.WriteString("\n\n")
	b.WriteString(content)
	b.WriteString("\n")
	if help != "" {
		b.WriteString("\n" + footerStyle.Render(help))
	}
	return appPad.Render(b.String())
}

// renderHelp is a small convenience so screens don't each need to know
// help.Model internals — just hand it the shared model and the current
// screen's keymap.
func renderHelp(h help.Model, km help.KeyMap) string {
	return h.View(km)
}
