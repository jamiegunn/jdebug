package main

// workload.go — Kubernetes object context for the selected pod. The main
// dashboard keeps Events available through commands, but spends the always-on
// space on ownership and storage so pod/container/JVM boundaries stay visible.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m model) workloadRows(w, h int) []string {
	d := m.panel
	rows := []string{paneTitle(w, "WORKLOAD", podShort(m.t.Pod, 20), "pod context")}
	line := func(k, v string) {
		if len(rows) >= h {
			return
		}
		if v == "" {
			v = "-"
		}
		rows = append(rows, ansi.Truncate(" "+cFaint.Render(fmt.Sprintf("%-8s", k))+cMuted.Render(v), w, "…"))
	}

	owner := d.OwnerName
	if d.OwnerKind != "" && d.OwnerName != "" {
		owner = d.OwnerKind + "/" + d.OwnerName
	}
	if d.DeployName != "" {
		line("deploy", d.DeployName)
	} else {
		line("deploy", "-")
	}
	line("owner", owner)
	line("node", d.NodeName)
	line("sa", d.ServiceAcct)

	if len(rows) < h {
		vols := "-"
		if len(d.Volumes) > 0 {
			vols = strings.Join(d.Volumes, ", ")
		}
		line("volumes", vols)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
