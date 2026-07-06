package main

import "testing"

const sampleScrape = `@@jvm.threads.live
{"name":"jvm.threads.live","measurements":[{"statistic":"VALUE","value":142}]}
@@jvm.gc.pause
{"name":"jvm.gc.pause","measurements":[{"statistic":"COUNT","value":10},{"statistic":"TOTAL_TIME","value":0.5},{"statistic":"MAX","value":0.05}]}
@@http.server.requests
{"name":"http.server.requests","measurements":[{"statistic":"COUNT","value":1000},{"statistic":"TOTAL_TIME","value":50.0}]}
@@hikaricp.connections.active
{"name":"hikaricp.connections.active","measurements":[{"statistic":"VALUE","value":8}]}
@@hikaricp.connections.idle
{"name":"hikaricp.connections.idle","measurements":[{"statistic":"VALUE","value":2}]}
@@hikaricp.connections.pending
{"name":"hikaricp.connections.pending","measurements":[{"statistic":"VALUE","value":0}]}
`

func TestParseActuatorMetrics(t *testing.T) {
	am := parseActuatorMetrics([]byte(sampleScrape))
	if am.Threads != 142 {
		t.Errorf("threads = %d, want 142", am.Threads)
	}
	if am.GCCount != 10 || am.GCTimeSec != 0.5 {
		t.Errorf("gc = %v/%v, want 10/0.5", am.GCCount, am.GCTimeSec)
	}
	if am.HTTPCount != 1000 || am.HTTPTimeSec != 50 {
		t.Errorf("http = %v/%v, want 1000/50", am.HTTPCount, am.HTTPTimeSec)
	}
	if am.DBActive != 8 || am.DBIdle != 2 || am.DBPending != 0 {
		t.Errorf("db = %d/%d/%d, want 8/2/0", am.DBActive, am.DBIdle, am.DBPending)
	}
}

// a missing meter (app doesn't expose it) must stay -1, not 0
func TestParseActuatorMetricsMissing(t *testing.T) {
	am := parseActuatorMetrics([]byte("@@jvm.threads.live\n{\"name\":\"x\",\"measurements\":[{\"statistic\":\"VALUE\",\"value\":50}]}\n"))
	if am.Threads != 50 {
		t.Fatalf("threads = %d, want 50", am.Threads)
	}
	if am.DBActive != -1 || am.GCCount != -1 || am.HTTPCount != -1 {
		t.Fatalf("unscraped meters must stay -1, got db=%d gc=%v http=%v", am.DBActive, am.GCCount, am.HTTPCount)
	}
}

func TestDeriveMetricsRates(t *testing.T) {
	am := actuatorMetrics{Threads: 142, GCCount: 10, GCTimeSec: 0.5,
		HTTPCount: 1000, HTTPTimeSec: 50, DBActive: 8, DBIdle: 2, DBPending: 0}
	// first sample: no previous counters (dt=0) → rates unknown, gauges direct
	f := deriveMetrics(am, -1, -1, -1, -1, 0)
	if f.Threads != 142 || f.DBActive != 8 {
		t.Errorf("gauges should pass through: %+v", f)
	}
	if f.GCPerMin != -1 || f.HTTPRps != -1 {
		t.Errorf("rates must be unknown on the first sample: %+v", f)
	}
	// next sample 20s later: 6 more collections (0.12s), 600 more requests (30s)
	am2 := actuatorMetrics{Threads: 150, GCCount: 16, GCTimeSec: 0.62,
		HTTPCount: 1600, HTTPTimeSec: 80, DBActive: 9, DBIdle: 1, DBPending: 3}
	g := deriveMetrics(am2, 10, 0.5, 1000, 50, 20)
	if g.GCPerMin != 18 { // 6 / 20s * 60
		t.Errorf("gc/min = %d, want 18", g.GCPerMin)
	}
	if g.GCPauseMs != 20 { // 0.12s / 6 = 20ms
		t.Errorf("gc pause = %dms, want 20", g.GCPauseMs)
	}
	if g.HTTPRps != 30 { // 600 / 20s
		t.Errorf("http rps = %d, want 30", g.HTTPRps)
	}
	if g.HTTPMs != 50 { // 30s / 600 = 50ms
		t.Errorf("http latency = %dms, want 50", g.HTTPMs)
	}
}

// a counter that goes backwards (the pod restarted) must not emit a negative rate
func TestDeriveMetricsCounterReset(t *testing.T) {
	am := actuatorMetrics{GCCount: 2, GCTimeSec: 0.1, HTTPCount: 5, HTTPTimeSec: 0.2,
		Threads: -1, DBActive: -1, DBIdle: -1, DBPending: -1}
	f := deriveMetrics(am, 100, 5.0, 900, 40, 20) // prev counters far higher → reset
	if f.GCPerMin != 0 || f.HTTPRps != 0 {
		t.Errorf("a counter reset must yield a 0 rate, not negative: %+v", f)
	}
}
