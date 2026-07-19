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

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type sample struct {
	When      time.Time
	MemPct    int // container memory as % of limit; -1 unknown
	CPUMilli  int // -1 unknown
	Restarts  int
	HeapPct   int // JVM heap used as % of max; -1 unknown
	Threads   int // jvm.threads.live; -1 unknown
	GCPauseMs int // avg GC pause since the last sample (ms); -1 unknown
	GCPerMin  int // collections/min since the last sample; -1 unknown
	HTTPRps   int // http requests/sec since the last sample; -1 unknown
	HTTPMs    int // avg request latency since the last sample (ms); -1 unknown
	DBActive  int // hikaricp active connections; -1 unknown
	DBIdle    int // hikaricp idle connections; -1 unknown
	DBPending int // hikaricp threads awaiting a connection; -1 unknown
}

// parseMi parses a kubectl/actuator size like "121Mi", "1.5Gi", "512Mi" into
// mebibytes. Returns -1 when it can't. Used to turn heap used/max into a %.
func parseMi(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "Gi"):
		mult, s = 1024, strings.TrimSuffix(s, "Gi")
	case strings.HasSuffix(s, "Mi"):
		mult, s = 1, strings.TrimSuffix(s, "Mi")
	case strings.HasSuffix(s, "Ki"):
		mult, s = 1.0/1024, strings.TrimSuffix(s, "Ki")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v * mult
}

// pct returns used/max as an int percent, or -1 when either is unparseable.
func pct(used, max string) int {
	u, m := parseMi(used), parseMi(max)
	if u < 0 || m <= 0 {
		return -1
	}
	return int(u * 100 / m)
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

func dashOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// metricRow renders one full-width line: an 8-col label, a wide sparkline
// scaled lo..hi, and the current value (right-aligned) in the row's style.
// stretchTo widens a short series to n points by nearest-neighbour repeat, so a
// full-width TRENDS bar reads as one solid band across the row instead of a stub
// with a wide dead gap before the value (the ~120-col empty middle on a 200-col
// monitor). Series already at/above n points are returned untouched.
func stretchTo(vals []int, n int) []int {
	if len(vals) == 0 || n <= len(vals) {
		return vals
	}
	out := make([]int, n)
	for i := range out {
		out[i] = vals[i*len(vals)/n]
	}
	return out
}

func metricRow(w int, label string, vals []int, lo, hi int, cur string, st lipgloss.Style) string {
	chartW := w - 10 - lipgloss.Width(cur) - 3
	if chartW < 8 {
		chartW = 8
	}
	// fill the whole chart width so the bar is a continuous band right up to the
	// value, instead of a short stub with dead space between it and the number
	left := " " + cFaint.Render(fmt.Sprintf("%-8s", label)) + st.Render(spark(stretchTo(vals, chartW), lo, hi, chartW))
	pad := w - lipgloss.Width(left) - lipgloss.Width(cur) - 1
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + st.Render(cur)
}

// restartRow renders restarts as ▲ markers (a restart sparkline would be flat
// 99% of the time) plus the running total.
func (m model) restartRow(w int) string {
	var rst []int
	for _, s := range m.hist {
		rst = append(rst, s.Restarts)
	}
	chartW := w - 10 - 12 - 3
	if chartW < 8 {
		chartW = 8
	}
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
	rval := "0 total"
	if len(rst) > 0 {
		rval = fmt.Sprintf("%d total", rst[len(rst)-1])
	}
	left := " " + cFaint.Render(fmt.Sprintf("%-8s", "restarts")) + marks.String()
	pad := w - lipgloss.Width(left) - lipgloss.Width(rval) - 1
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + cMuted.Render(rval)
}

// metricsTabRows renders the full-width TRENDS/metrics tab: one wide sparkline
// per metric with its live value. Core rows (heap, mem, cpu, restarts) always
// show; the actuator-sourced rows (threads, gc, http, db) appear only once
// there's data, so the tab never shows a wall of "–".
func (m model) metricsTabRows(w, h int) []string {
	rows := []string{paneTitle(w, "TRENDS", "", "")} // row 0 is replaced by the tab strip

	series := func(pick func(sample) int) []int {
		out := make([]int, 0, len(m.hist))
		for _, s := range m.hist {
			out = append(out, pick(s))
		}
		return out
	}
	last := func(v []int) int {
		if len(v) == 0 {
			return -1
		}
		return v[len(v)-1]
	}
	hiOf := func(v []int, floor int) int {
		hi := floor
		for _, x := range v {
			if x > hi {
				hi = x
			}
		}
		if hi <= 0 {
			hi = 1
		}
		return hi
	}
	hasData := func(v []int) bool {
		for _, x := range v {
			if x >= 0 {
				return true
			}
		}
		return false
	}
	pctStyle := func(p int) lipgloss.Style {
		switch {
		case p >= 90:
			return cDisr
		case p >= 75:
			return cWarn
		default:
			return cMuted
		}
	}

	var body []string

	// JVM heap — the headline for a memory tool
	heap := series(func(s sample) int { return s.HeapPct })
	hCur := "–"
	if last(heap) >= 0 {
		hCur = fmt.Sprintf("%d%%", last(heap))
		if m.panel.HeapUsed != "" {
			hCur += " · " + m.panel.HeapUsed + "/" + dashOr(m.panel.HeapMax, "?") + " " + m.panel.HeapVia
		}
	}
	body = append(body, metricRow(w, "heap", heap, 0, 100, hCur, pctStyle(last(heap))))

	// container memory as % of limit
	mem := series(func(s sample) int { return s.MemPct })
	mCur := "–"
	if last(mem) >= 0 {
		mCur = fmt.Sprintf("%d%%", last(mem))
		if m.panel.MemUse != "" {
			mCur += " · " + m.panel.MemUse + " of " + m.panel.MemLimit + " limit"
		}
	}
	body = append(body, metricRow(w, "mem", mem, 0, 100, mCur, pctStyle(last(mem))))

	// CPU vs limit (fallback: max observed)
	cpu := series(func(s sample) int { return s.CPUMilli })
	cpuHi := cpuMilli(m.panel.CPULimit)
	if cpuHi <= 0 {
		cpuHi = hiOf(cpu, 1)
	}
	cCur := "–"
	if last(cpu) >= 0 {
		cCur = dashOr(m.panel.CPUUse, "?")
		if m.panel.CPULimit != "" {
			cCur += " of " + m.panel.CPULimit + " limit"
		}
	}
	body = append(body, metricRow(w, "cpu", cpu, 0, cpuHi, cCur, cMuted))

	// threads (actuator)
	if thr := series(func(s sample) int { return s.Threads }); hasData(thr) {
		body = append(body, metricRow(w, "threads", thr, 0, hiOf(thr, 8),
			fmt.Sprintf("%d live", last(thr)), cMuted))
	}

	// GC pause + frequency (actuator)
	if gcp := series(func(s sample) int { return s.GCPauseMs }); hasData(gcp) {
		gcr := series(func(s sample) int { return s.GCPerMin })
		cur := fmt.Sprintf("%dms avg pause", last(gcp))
		if last(gcr) >= 0 {
			cur += fmt.Sprintf(" · %d/min", last(gcr))
		}
		st := cMuted
		if last(gcp) >= 200 {
			st = cWarn
		}
		if last(gcp) >= 500 {
			st = cDisr
		}
		body = append(body, metricRow(w, "gc", gcp, 0, hiOf(gcp, 50), cur, st))
	}

	// HTTP throughput + latency (actuator)
	if hr := series(func(s sample) int { return s.HTTPRps }); hasData(hr) {
		hl := series(func(s sample) int { return s.HTTPMs })
		cur := fmt.Sprintf("%d req/s", last(hr))
		if last(hl) >= 0 {
			cur += fmt.Sprintf(" · %dms avg", last(hl))
		}
		body = append(body, metricRow(w, "http", hr, 0, hiOf(hr, 1), cur, cMuted))
	}

	// DB connection pool (HikariCP, actuator)
	if dba := series(func(s sample) int { return s.DBActive }); hasData(dba) {
		idle := last(series(func(s sample) int { return s.DBIdle }))
		pend := last(series(func(s sample) int { return s.DBPending }))
		cur := fmt.Sprintf("%d active", last(dba))
		if idle >= 0 {
			cur += fmt.Sprintf(" · %d idle", idle)
		}
		if pend > 0 {
			cur += fmt.Sprintf(" · %d waiting", pend)
		}
		st := cMuted
		if pend > 0 {
			st = cWarn
		}
		body = append(body, metricRow(w, "db pool", dba, 0, hiOf(dba, 4), cur, st))
	}

	body = append(body, m.restartRow(w))

	if len(m.hist) < 2 {
		body = append(body, " "+cFaint.Render(ansi.Truncate(
			"collecting… one sample per 20s refresh — leave the dashboard open to see the trend", w-2, "…")))
	}

	rows = append(rows, body...)
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
