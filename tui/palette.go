package main

// The readability-tuned GitHub-dark palette (same values as the bash TUI's
// truecolor branch). Lipgloss degrades to 256/16 colors automatically.

import "github.com/charmbracelet/lipgloss"

var (
	cTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc"))
	cBody  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e6edf3"))
	cMuted = lipgloss.NewStyle().Foreground(lipgloss.Color("#b6c2cf"))
	cDim   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#9ea7b1"))
	cFaint = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	cKey   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#79c0ff"))
	cAcc   = lipgloss.NewStyle().Foreground(lipgloss.Color("#1f6feb"))
	cRule  = lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
	cSafe  = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950"))
	cCaut  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341"))
	cDisr  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72"))
	cOK    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3fb950"))
	cWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341"))
	cFocus = lipgloss.NewStyle().Background(lipgloss.Color("#161b22"))
)
