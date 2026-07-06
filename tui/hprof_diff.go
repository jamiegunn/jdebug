package main

// hprof_diff.go — compare two heap dumps taken at different times (ideally the
// same app under sustained load). A single dump shows a snapshot; a DIFF shows
// GROWTH — which classes gained the most objects/bytes between the two. Steady
// growth of one class across dumps is the surest leak signal there is, and it's
// exactly MAT's "compare to another heap dump". Reuses the shallow histogram.

import (
	"fmt"
	"sort"
	"strings"
)

func classMap(h *heapHistogram) map[string]classStat {
	m := make(map[string]classStat, len(h.classes))
	for _, cs := range h.classes {
		m[cs.name] = cs
	}
	return m
}

type classDiff struct {
	name           string
	dCount, dBytes int64
}

func analyzeHprofDiff(before, after string) (string, error) {
	hA, err := analyzeHprof(before)
	if err != nil {
		return "", fmt.Errorf("reading the BEFORE dump: %w", err)
	}
	hB, err := analyzeHprof(after)
	if err != nil {
		return "", fmt.Errorf("reading the AFTER dump: %w", err)
	}
	a, b := classMap(hA), classMap(hB)

	var diffs []classDiff
	for name, cs := range b {
		diffs = append(diffs, classDiff{name, cs.count - a[name].count, cs.bytes - a[name].bytes})
	}
	for name, cs := range a { // classes that vanished entirely
		if _, ok := b[name]; !ok {
			diffs = append(diffs, classDiff{name, -cs.count, -cs.bytes})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].dBytes > diffs[j].dBytes })

	var b2 strings.Builder
	fmt.Fprintf(&b2, "heap GROWTH — before → after (what changed between two dumps)\n")
	fmt.Fprintf(&b2, "before %s / %s objects   after %s / %s objects   Δ %s\n\n",
		fmtSize(hA.totalBytes), humanCount(hA.totalObjs),
		fmtSize(hB.totalBytes), humanCount(hB.totalObjs), signedSize(hB.totalBytes-hA.totalBytes))

	b2.WriteString("grew most (Δbytes | Δobjects | class) — the leak suspects:\n")
	shown := 0
	for _, d := range diffs {
		if d.dBytes <= 0 || shown >= 12 {
			break
		}
		fmt.Fprintf(&b2, "  %10s  %10s  %s\n", signedSize(d.dBytes), signedCount(d.dCount), d.name)
		shown++
	}
	if shown == 0 {
		b2.WriteString("  (nothing grew — the after-dump is not larger than the before-dump)\n")
	}

	// biggest shrinkers, if any (things that were freed — reassuring context)
	var shrank []classDiff
	for i := len(diffs) - 1; i >= 0 && len(shrank) < 4; i-- {
		if diffs[i].dBytes < 0 {
			shrank = append(shrank, diffs[i])
		}
	}
	if len(shrank) > 0 {
		b2.WriteString("\nshrank most (freed between dumps):\n")
		for _, d := range shrank {
			fmt.Fprintf(&b2, "  %10s  %10s  %s\n", signedSize(d.dBytes), signedCount(d.dCount), d.name)
		}
	}

	b2.WriteString("\n" + diffVerdict(diffs, hB.totalBytes-hA.totalBytes) + "\n")
	b2.WriteString("a class that grows across EVERY dump under steady load is your leak. Confirm the\n")
	b2.WriteString("winner with:  jdebug analyze --deep <after>  (retained size + path to GC roots).")
	return b2.String(), nil
}

func diffVerdict(diffs []classDiff, totalDelta int64) string {
	if len(diffs) == 0 || diffs[0].dBytes <= 0 {
		return "verdict: nothing grew — no leak visible between these two dumps (or they're the same)."
	}
	// how concentrated is the growth?
	var grown int64
	for _, d := range diffs {
		if d.dBytes > 0 {
			grown += d.dBytes
		}
	}
	top := diffs[0]
	share := 0.0
	if grown > 0 {
		share = float64(top.dBytes) * 100 / float64(grown)
	}
	switch {
	case share >= 40 && !isFrameworkClass(top.name):
		return fmt.Sprintf("verdict: LEAK SUSPECT — %s grew by %s (%.0f%% of all growth), and it's your own\n"+
			"  type. If it climbs again on a third dump, that's the leak. Deep-analyze the after dump.", top.name, signedSize(top.dBytes), share)
	case share >= 40:
		return fmt.Sprintf("verdict: %s grew most (%s, %.0f%% of growth) but it's a framework/JDK type —\n"+
			"  often a warming cache/pool. Watch whether it keeps growing or levels off.", top.name, signedSize(top.dBytes), share)
	case totalDelta <= 0:
		return "verdict: the heap didn't grow overall — this looks like normal churn, not a leak."
	default:
		return "verdict: growth is spread across many classes rather than one runaway type — more like\n" +
			"  warm-up/caching than a single leak. Take a third dump later and diff again to be sure."
	}
}

func signedSize(n int64) string {
	if n < 0 {
		return "-" + fmtSize(-n)
	}
	return "+" + fmtSize(n)
}

func signedCount(n int64) string {
	if n < 0 {
		return "-" + humanCount(-n)
	}
	return "+" + humanCount(n)
}
