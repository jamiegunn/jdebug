package main

// captures.go — the dumps/ browser pane: every artifact (thread dumps, heap
// dumps, snapshot bundles) with size and age, newest first. Local FS only —
// cheap enough for the 20s tick. Also the source of truth for "is there
// evidence yet" in the NEXT suggestions.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type capEntry struct {
	Name string
	Size int64
	Mod  time.Time
	Dir  bool
}

type capsMsg []capEntry

func fetchCaps(kit string) tea.Cmd {
	return func() tea.Msg {
		dir := dumpsDir(kit)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return capsMsg(nil)
		}
		var caps []capEntry
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "session-") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			ce := capEntry{Name: e.Name(), Mod: info.ModTime(), Dir: e.IsDir()}
			if ce.Dir {
				ce.Size = dirSize(filepath.Join(dir, e.Name()))
			} else {
				ce.Size = info.Size()
			}
			caps = append(caps, ce)
		}
		sort.Slice(caps, func(i, j int) bool { return caps[i].Mod.After(caps[j].Mod) })
		return capsMsg(caps)
	}
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// capsRows renders exactly h rows at width w.
func (m model) capsRows(w, h int) []string {
	rows := []string{paneTitle(w, "CAPTURES", "dumps/", "[d] browse · [a] analyze")}
	if len(m.caps) == 0 {
		rows = append(rows, " "+cFaint.Render("– nothing captured yet –"))
	}
	for _, ce := range m.caps {
		if len(rows) >= h {
			break
		}
		name := ce.Name
		if ce.Dir {
			name += "/"
		}
		right := fmt.Sprintf("%6s %4s", fmtSize(ce.Size), fmtAge(ce.Mod))
		nameW := w - len(right) - 3
		if nameW < 8 {
			nameW = 8
		}
		name = ansi.Truncate(name, nameW, "…")
		pad := w - 1 - lipgloss.Width(name) - len(right) - 1
		if pad < 1 {
			pad = 1
		}
		rows = append(rows, " "+cMuted.Render(name)+strings.Repeat(" ", pad)+cFaint.Render(right))
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
