package main

// artifacts.go — remote-artifact transparency + cleanup (u). In remote mode the
// jattach tier and push-local COPY a small file into the pod (/tmp/jattach,
// /tmp/jdebug-local). A junior may not realise the TUI wrote anything into a
// pod — it matters in locked-down clusters and security reviews. The CLI records
// what it staged in a manifest (lib/common.sh record_artifact); this reads it,
// shows a footer indicator, and offers cleanup (which never removes pre-existing
// files or local dumps/).

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type artifact struct {
	owned bool // jdebug staged it this session (offer to remove) vs pre-existing
	pod   string
	path  string
	note  string
}

type artifactsMsg struct{ list []artifact }

func fetchArtifacts(kit string) tea.Cmd {
	mf := filepath.Join(dumpsDir(kit), "remote-artifacts.tsv")
	return func() tea.Msg {
		data, err := os.ReadFile(mf)
		if err != nil {
			return artifactsMsg{} // no manifest = nothing staged
		}
		var out []artifact
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			f := strings.Split(line, "\t")
			if len(f) < 5 || f[4] == "" {
				continue
			}
			note := ""
			if len(f) >= 6 {
				note = f[5]
			}
			out = append(out, artifact{owned: f[0] == "1", pod: f[2], path: f[4], note: note})
		}
		return artifactsMsg{list: out}
	}
}

// ownedArtifacts counts the files jdebug staged this session (removable).
func (m model) ownedArtifacts() int {
	n := 0
	for _, a := range m.artifacts {
		if a.owned {
			n++
		}
	}
	return n
}

func (m model) cleanupView() string {
	var b strings.Builder
	b.WriteString("\n  " + cTitle.Render("REMOTE ARTIFACTS") + "\n")
	b.WriteString("  " + cFaint.Render("Intent: review (and optionally remove) the files jdebug copied INTO the pod this session.") + "\n\n")
	staged := m.ownedArtifacts()
	if len(m.artifacts) == 0 {
		b.WriteString("    " + cOK.Render("✓ nothing staged") + cMuted.Render(" — jdebug hasn't copied anything into a pod this session.") + "\n")
	} else {
		for _, a := range m.artifacts {
			if a.owned {
				b.WriteString("    " + cWarn.Render("● "+a.path) + cMuted.Render("  in "+a.pod+" — "+a.note+" ") + cFaint.Render("(staged by jdebug)") + "\n")
			} else {
				b.WriteString("    " + cMuted.Render("· "+a.path+"  in "+a.pod+" — "+a.note+" ") + cFaint.Render("(pre-existing — kept)") + "\n")
			}
		}
	}
	b.WriteString("\n  " + cFaint.Render("kept safe: files that existed before this session · anything jdebug didn't stage · local dumps/") + "\n")
	if staged > 0 {
		b.WriteString("  " + cFaint.Render("will run: ") + cMuted.Render("jdebug cleanup --confirm  (kubectl exec … rm -f the staged paths)") + "\n")
		b.WriteString("\n  " + cWarn.Render("y clean up now") + cFaint.Render(" · esc keep for now") + " ")
	} else {
		b.WriteString("\n  " + cFaint.Render("any key returns") + " ")
	}
	return b.String()
}

func (m model) cleanupKey(key string) (tea.Model, tea.Cmd) {
	// y/Y only — NOT enter: this runs `rm -f` inside the pod, and enter is the
	// benign "dismiss" key everywhere else, so a habitual enter must not fire it
	if (key == "y" || key == "Y") && m.ownedArtifacts() > 0 {
		m.scr = scMenu
		return m.quickCLI(false, "cleanup", "--confirm")
	}
	m.scr = scMenu
	return m, nil
}
