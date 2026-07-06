package main

// metrics.go — scrape the Spring Boot actuator /metrics endpoints for the
// TRENDS tab: live thread count, GC pause/frequency, HTTP throughput/latency,
// and the HikariCP connection pool. One in-pod exec per refresh (six quick
// local curls), all read-only. Anything the app doesn't expose stays -1 and
// its row simply doesn't appear — never a wall of "–".

import (
	"encoding/json"
	"os/exec"
	"strings"
)

// actuatorMetrics is the raw scrape. GC/HTTP are cumulative counters the tick
// handler turns into rates; the rest are point-in-time gauges. Negative = N/A.
type actuatorMetrics struct {
	Threads     int
	GCCount     float64
	GCTimeSec   float64
	HTTPCount   float64
	HTTPTimeSec float64
	DBActive    int
	DBIdle      int
	DBPending   int
}

func emptyMetrics() actuatorMetrics {
	return actuatorMetrics{Threads: -1, GCCount: -1, GCTimeSec: -1,
		HTTPCount: -1, HTTPTimeSec: -1, DBActive: -1, DBIdle: -1, DBPending: -1}
}

var metricNames = []string{
	"jvm.threads.live", "jvm.gc.pause", "http.server.requests",
	"hikaricp.connections.active", "hikaricp.connections.idle", "hikaricp.connections.pending",
}

type meterResp struct {
	Name         string `json:"name"`
	Measurements []struct {
		Statistic string  `json:"statistic"`
		Value     float64 `json:"value"`
	} `json:"measurements"`
}

func (r meterResp) stat(name string) (float64, bool) {
	for _, m := range r.Measurements {
		if m.Statistic == name {
			return m.Value, true
		}
	}
	return 0, false
}

// jvmMetrics scrapes the six actuator meters in ONE exec. Errors → empty (all
// rows hidden). Only worth calling when the actuator answered the heap read.
func jvmMetrics(t target) actuatorMetrics {
	if t.Actuator == "" {
		return emptyMetrics()
	}
	snippet := "A='" + t.Actuator + "'\n" +
		"for M in " + strings.Join(metricNames, " ") + "; do echo \"@@$M\"; " +
		"if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 3 \"$A/metrics/$M\" 2>/dev/null; " +
		"else wget -qO- \"$A/metrics/$M\" 2>/dev/null; fi; echo; done"
	out, err := exec.Command("kubectl", "-n", t.Namespace, "exec", t.Pod, "-c", t.Container,
		"--", "sh", "-c", snippet).Output()
	if err != nil {
		return emptyMetrics()
	}
	return parseActuatorMetrics(out)
}

// parseActuatorMetrics splits the @@name-delimited scrape into per-meter JSON
// and pulls the statistics we chart. Pure, so it's unit-testable.
func parseActuatorMetrics(out []byte) actuatorMetrics {
	am := emptyMetrics()
	blocks := map[string]string{}
	var cur string
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			blocks[cur] = buf.String()
		}
		buf.Reset()
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "@@") {
			flush()
			cur = strings.TrimPrefix(line, "@@")
			continue
		}
		buf.WriteString(line)
	}
	flush()

	meter := func(name string) (meterResp, bool) {
		body := strings.TrimSpace(blocks[name])
		if !strings.HasPrefix(body, "{") {
			return meterResp{}, false
		}
		var r meterResp
		if json.Unmarshal([]byte(body), &r) != nil {
			return meterResp{}, false
		}
		return r, true
	}
	gauge := func(name string, dst *int) {
		if r, ok := meter(name); ok {
			if v, ok := r.stat("VALUE"); ok {
				*dst = int(v)
			}
		}
	}

	gauge("jvm.threads.live", &am.Threads)
	if r, ok := meter("jvm.gc.pause"); ok {
		if v, ok := r.stat("COUNT"); ok {
			am.GCCount = v
		}
		if v, ok := r.stat("TOTAL_TIME"); ok {
			am.GCTimeSec = v
		}
	}
	if r, ok := meter("http.server.requests"); ok {
		if v, ok := r.stat("COUNT"); ok {
			am.HTTPCount = v
		}
		if v, ok := r.stat("TOTAL_TIME"); ok {
			am.HTTPTimeSec = v
		}
	}
	gauge("hikaricp.connections.active", &am.DBActive)
	gauge("hikaricp.connections.idle", &am.DBIdle)
	gauge("hikaricp.connections.pending", &am.DBPending)
	return am
}

// metricFields is the derived per-sample metric values (rates + gauges).
type metricFields struct {
	Threads, GCPauseMs, GCPerMin, HTTPRps, HTTPMs, DBActive, DBIdle, DBPending int
}

// deriveMetrics turns a raw scrape plus the previous cumulative counters into a
// sample's metric fields. dtSec is seconds since the previous scrape (≤0 = no
// usable previous, so the rate fields stay -1). Pure and unit-testable.
func deriveMetrics(am actuatorMetrics, prevGCCount, prevGCTime, prevHTTPCount, prevHTTPTime, dtSec float64) metricFields {
	f := metricFields{Threads: am.Threads, GCPauseMs: -1, GCPerMin: -1, HTTPRps: -1, HTTPMs: -1,
		DBActive: am.DBActive, DBIdle: am.DBIdle, DBPending: am.DBPending}
	if am.GCCount >= 0 && prevGCCount >= 0 && dtSec > 0 {
		dc := am.GCCount - prevGCCount
		if dc < 0 { // counter reset (a restart) — skip the rate this tick
			dc = 0
		}
		f.GCPerMin = int(dc / dtSec * 60)
		if dc > 0 {
			f.GCPauseMs = int((am.GCTimeSec - prevGCTime) / dc * 1000)
		} else {
			f.GCPauseMs = 0
		}
	}
	if am.HTTPCount >= 0 && prevHTTPCount >= 0 && dtSec > 0 {
		dc := am.HTTPCount - prevHTTPCount
		if dc < 0 {
			dc = 0
		}
		f.HTTPRps = int(dc / dtSec)
		if dc > 0 {
			f.HTTPMs = int((am.HTTPTimeSec - prevHTTPTime) / dc * 1000)
		} else {
			f.HTTPMs = 0
		}
	}
	return f
}
