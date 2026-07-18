package core

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Memory-report diffing — the v2 fix for audit finding F9: native and
// off-heap leaks are visible only as GROWTH between two points in time, but
// v1 could diff heap dumps only, leaving the operator to eyeball two memory
// reports by hand during a multi-hour RSS creep. This parses the reports
// memory-report.sh writes and prints the deltas per bucket.

// reMemLine matches "  <label>  :  <num> MiB" rows (and the pool rows).
var reMemLine = regexp.MustCompile(`^\s{2,}([A-Za-z][^:]*?)\s*:\s*(-?[0-9]+(?:\.[0-9]+)?)\s*MiB`)

// ParseMemReport extracts label → MiB from one memory-report.txt.
// Later duplicates win (there are none in practice; pools are unique).
func ParseMemReport(r io.Reader) (map[string]float64, error) {
	out := map[string]float64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if m := reMemLine.FindStringSubmatch(sc.Text()); m != nil {
			v, err := strconv.ParseFloat(m[2], 64)
			if err == nil {
				out[strings.TrimSpace(m[1])] = v
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no memory rows found — is this a jdebug memory report?")
	}
	return out, sc.Err()
}

// IsMemReport sniffs whether a file looks like a jdebug memory report.
func IsMemReport(content string) bool {
	return strings.Contains(content, "Container RSS") && strings.Contains(content, "JVM heap")
}

// DiffMemReports renders before→after deltas, biggest growth first, with
// the two verdict lines an operator actually needs.
func DiffMemReports(before, after map[string]float64, w io.Writer) {
	type row struct {
		label   string
		b, a, d float64
		inBoth  bool
	}
	var rows []row
	for label, av := range after {
		if bv, ok := before[label]; ok {
			rows = append(rows, row{label, bv, av, av - bv, true})
		} else {
			rows = append(rows, row{label, 0, av, av, false})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].d != rows[j].d {
			return rows[i].d > rows[j].d
		}
		return rows[i].label < rows[j].label
	})
	fmt.Fprintf(w, "memory growth, before → after (MiB):\n")
	for _, r := range rows {
		mark := " "
		switch {
		case r.d >= 32:
			mark = "⚠"
		case r.d > 0:
			mark = "↑"
		case r.d < 0:
			mark = "↓"
		}
		note := ""
		if !r.inBoth {
			note = "  (new in the second report)"
		}
		fmt.Fprintf(w, "  %s %-32s %10.1f → %-10.1f %+8.1f%s\n", mark, r.label, r.b, r.a, r.d, note)
	}

	// the two questions this diff exists to answer
	heapD := after["used"] - before["used"] // JVM heap "used" row
	rssD := after["Container RSS"] - before["Container RSS"]
	unaccD := after["Unaccounted"] - before["Unaccounted"]
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Bottom line:")
	switch {
	case rssD <= 8:
		fmt.Fprintf(w, "  container memory grew %.1f MiB — no meaningful growth between these reports.\n", rssD)
	case heapD >= rssD*0.7:
		fmt.Fprintf(w, "  container grew %.1f MiB and the JVM heap accounts for %.1f MiB of it → the growth IS the heap.\n", rssD, heapD)
		fmt.Fprintln(w, "Next: a heap dump names the objects — jdebug heap --confirm (pauses the app), then jdebug analyze --diff")
	case unaccD >= rssD*0.5:
		fmt.Fprintf(w, "  container grew %.1f MiB but %.1f MiB of it is UNACCOUNTED (not heap, not pools) → suspect a NATIVE leak.\n", rssD, unaccD)
		fmt.Fprintln(w, "Next: enable NMT (-XX:NativeMemoryTracking=summary), then: jdebug jcmd 'VM.native_memory summary'")
	default:
		fmt.Fprintf(w, "  container grew %.1f MiB; heap +%.1f, unaccounted %+.1f — check the ↑ rows above (buffers? metaspace? threads?).\n", rssD, heapD, unaccD)
	}
}
