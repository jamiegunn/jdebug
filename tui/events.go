package main

// events.go — recent kubernetes events for the target pod (OOMKilled, probe
// failures, back-off, scheduling). Fetched on the 20s tick.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type eventLine struct {
	Age, Type, Reason, Msg string
}

type eventsMsg struct {
	lines []eventLine
	err   string
}

func fetchEvents(t target) tea.Cmd {
	return func() tea.Msg {
		if t.Pod == "" {
			return eventsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "get", "events",
			"--field-selector", "involvedObject.name="+t.Pod,
			"--sort-by=.lastTimestamp", "--no-headers").Output()
		if err != nil {
			return eventsMsg{err: firstLine(err.Error())}
		}
		var evs []eventLine
		for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			f := strings.Fields(ln)
			if len(f) < 5 {
				continue
			}
			evs = append(evs, eventLine{Age: f[0], Type: f[1], Reason: f[2], Msg: strings.Join(f[4:], " ")})
		}
		// sorted oldest→newest; keep the newest 12, newest first
		if len(evs) > 12 {
			evs = evs[len(evs)-12:]
		}
		for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
			evs[i], evs[j] = evs[j], evs[i]
		}
		return eventsMsg{lines: evs}
	}
}

// eventsRows renders exactly h rows at width w.
func (m model) eventsRows(w, h int) []string {
	rows := []string{paneTitle(w, "EVENTS", m.t.Pod, "20s")}
	switch {
	case m.eventsErr != "" && len(m.events) == 0:
		rows = append(rows, " "+cFaint.Render("– events unavailable: "+m.eventsErr+" –"))
	case len(m.events) == 0:
		rows = append(rows, " "+cFaint.Render("– no recent events –"))
	default:
		for _, ev := range m.events {
			if len(rows) >= h {
				break
			}
			mark, st := " ", cMuted
			if ev.Type == "Warning" {
				mark, st = "⚠", cWarn
			}
			line := " " + cFaint.Render(fmt.Sprintf("%-5s", ev.Age)) + st.Render(mark+" "+fmt.Sprintf("%-10s", ev.Reason)) +
				" " + cMuted.Render(ansi.Truncate(ev.Msg, w-21, "…"))
			rows = append(rows, ansi.Truncate(line, w, "…"))
		}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
