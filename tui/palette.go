package main

// Adaptive palette: every token has a dark-background variant (the
// readability-tuned GitHub-dark ramp) and a light-background variant
// (GitHub-light). Lipgloss detects the terminal background at startup and
// picks the right side; JDEBUG_THEME=light|dark overrides detection.
// Limited terminals degrade to 256/16 colors automatically.

import "github.com/charmbracelet/lipgloss"

func ac(dark, light string) lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Dark: dark, Light: light}
}

var (
	cTitle = lipgloss.NewStyle().Bold(true).Foreground(ac("#f0f6fc", "#1f2328"))
	cBody  = lipgloss.NewStyle().Bold(true).Foreground(ac("#e6edf3", "#1f2328"))
	cMuted = lipgloss.NewStyle().Foreground(ac("#b6c2cf", "#424a53"))
	cDim   = lipgloss.NewStyle().Bold(true).Foreground(ac("#9ea7b1", "#59636e"))
	cFaint = lipgloss.NewStyle().Foreground(ac("#8b949e", "#6e7781"))
	cKey   = lipgloss.NewStyle().Bold(true).Foreground(ac("#79c0ff", "#0969da"))
	cAcc   = lipgloss.NewStyle().Foreground(ac("#1f6feb", "#0969da"))
	cRule  = lipgloss.NewStyle().Foreground(ac("#30363d", "#d1d9e0"))
	cSafe  = lipgloss.NewStyle().Foreground(ac("#3fb950", "#1a7f37"))
	cCaut  = lipgloss.NewStyle().Foreground(ac("#e3b341", "#9a6700"))
	cDisr  = lipgloss.NewStyle().Bold(true).Foreground(ac("#ff7b72", "#d1242f"))
	cOK    = lipgloss.NewStyle().Bold(true).Foreground(ac("#3fb950", "#1a7f37"))
	cWarn  = lipgloss.NewStyle().Foreground(ac("#e3b341", "#9a6700"))
	cFocus = lipgloss.NewStyle().Background(ac("#161b22", "#f6f8fa"))
)

// applyTheme honors JDEBUG_THEME=light|dark, overriding background detection.
func applyTheme(theme string) {
	switch theme {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
}
