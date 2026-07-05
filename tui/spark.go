package main

// spark.go — tiny inline history charts. Samples piggyback on the panel
// fetch (one per refresh, ~20s apart), so trends cost no extra kubectl
// calls. Restarts render as ▲ markers where the count incremented — a
// restart *sparkline* would be a flat line 99% of the time.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type sample struct {
	When     time.Time
	MemPct   int // -1 unknown
	CPUMilli int // -1 unknown
	Restarts int
}

const histCap = 90 // ~30 minutes at one sample per 20s

func pushSample(h []sample, s sample) []sample {
	h = append(h, s)
	if len(h) > histCap {
		h = h[len(h)-histCap:]
	}
	return h
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// spark scales vals into ▁..█ over lo..hi; negatives render as gaps. Only
// the last w samples are shown.
func spark(vals []int, lo, hi, w int) string {
	if w <= 0 || hi <= lo {
		return ""
	}
	start := 0
	if len(vals) > w {
		start = len(vals) - w
	}
	var b strings.Builder
	for _, v := range vals[start:] {
		if v < 0 {
			b.WriteRune(' ')
			continue
		}
		i := (v - lo) * 7 / (hi - lo)
		if i < 0 {
			i = 0
		}
		if i > 7 {
			i = 7
		}
		b.WriteRune(sparkBlocks[i])
	}
	return b.String()
}

// cpuMilli parses kubectl cpu quantities: "250m" → 250, "1" → 1000.
func cpuMilli(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	if strings.HasSuffix(s, "m") {
		if v, err := strconv.Atoi(strings.TrimSuffix(s, "m")); err == nil {
			return v
		}
		return -1
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return int(v * 1000)
	}
	return -1
}

// trendsRows renders the TRENDS section for a w-wide column.
func (m model) trendsRows(w int) []string {
	fill := w - 10
	if fill < 3 {
		fill = 3
	}
	rows := []string{" " + cDim.Render("TRENDS") + "  " + cRule.Render(strings.Repeat("─", fill))}

	chartW := w - 13
	if chartW < 8 {
		chartW = 8
	}
	var mem, cpu, rst []int
	for _, s := range m.hist {
		mem = append(mem, s.MemPct)
		cpu = append(cpu, s.CPUMilli)
		rst = append(rst, s.Restarts)
	}

	// memory as % of limit
	last := -1
	if len(mem) > 0 {
		last = mem[len(mem)-1]
	}
	vst := cMuted
	if last >= 90 {
		vst = cDisr
	} else if last >= 75 {
		vst = cWarn
	}
	val := "–"
	if last >= 0 {
		val = fmt.Sprintf("%d%%", last)
	}
	rows = append(rows, " "+cFaint.Render("mem   ")+vst.Render(spark(mem, 0, 100, chartW))+" "+vst.Render(val))

	// cpu scaled against the limit (fallback: max observed)
	hi := cpuMilli(m.panel.CPULimit)
	if hi <= 0 {
		for _, v := range cpu {
			if v > hi {
				hi = v
			}
		}
	}
	if hi <= 0 {
		hi = 1
	}
	cval := "–"
	if len(cpu) > 0 && cpu[len(cpu)-1] >= 0 {
		cval = m.panel.CPUUse
	}
	rows = append(rows, " "+cFaint.Render("cpu   ")+cMuted.Render(spark(cpu, 0, hi, chartW))+" "+cMuted.Render(cval))

	// restart markers
	start := 0
	if len(rst) > chartW {
		start = len(rst) - chartW
	}
	var marks strings.Builder
	for i := start; i < len(rst); i++ {
		if i > 0 && rst[i] > rst[i-1] && rst[i-1] >= 0 {
			marks.WriteString(cDisr.Render("▲"))
		} else {
			marks.WriteString(cFaint.Render("·"))
		}
	}
	rval := "–"
	if len(rst) > 0 {
		rval = fmt.Sprintf("%d", rst[len(rst)-1])
	}
	rows = append(rows, " "+cFaint.Render("rst   ")+marks.String()+" "+cMuted.Render(rval))

	// a legend so the sparklines aren't a mystery: what each row is, that
	// values are point-in-time samples (not averages), and the cadence/gaps
	legend := "mem=%limit cpu=vs-limit ▲=restart · point-in-time, 1/20s"
	if len(m.hist) < 2 {
		legend = "collecting… 1 point per 20s refresh · mem=%limit ▲=restart"
	}
	rows = append(rows, " "+cFaint.Render(ansi.Truncate(legend, w-2, "…")))
	return rows
}
