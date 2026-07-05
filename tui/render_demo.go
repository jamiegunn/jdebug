package main

// render_demo.go — `-render <screen>` prints a screen with canned state and
// exits. No kubectl, no tty: this is how the kit's test suite asserts parity
// between the Go and bash frontends. `menu` stays at 120 cols / unmeasured
// height on purpose — tier 1, byte-identical to the classic layout.

import "time"

var ctxOverride string

func demoModel() model {
	m := model{
		kit:   ".",
		mode:  1,
		width: 120,
		t: target{Namespace: "debug-demo", Selector: "", Container: "app",
			Actuator: "http://localhost:8080/actuator", Pod: "app-debug-demo-app-6c6c4b5769-s9jdg"},
	}
	ctxOverride = "ddk3s"
	m.remote = probe{OK: true, Cluster: true, When: time.Now().Add(time.Hour)}
	m.panel = panelData{When: time.Now(), Phase: "Running", Waiting: "CrashLoopBackOff",
		Restarts: 34, LastReason: "OOMKilled",
		MemUse: "480Mi", MemLimit: "512Mi", MemPct: 94, CPUUse: "250m", CPULimit: "500m",
		HPAName: "app-debug-demo-app", HPACur: 6, HPAMax: 6, HPAMin: 2,
		HeapUsed: "121Mi", HeapMax: "1732Mi", HeapVia: "actuator", ActuatorOK: true}
	m.local = probe{OK: true, Jattach: true, When: time.Now().Add(time.Hour),
		Lines: []string{cSafe.Render("   ✓") + cMuted.Render(" actuator answering"), cSafe.Render("   ✓") + cMuted.Render(" jattach staged")}}

	// live-pane demo data: a memory ramp with one restart, a stack trace in
	// the logs, a back-off event, three captures.
	for i := 0; i < 20; i++ {
		s := sample{When: time.Now(), MemPct: 60 + i*2, CPUMilli: 120 + i*7, Restarts: 33}
		if i >= 12 {
			s.Restarts = 34
		}
		m.hist = pushSample(m.hist, s)
	}
	m.events = []eventLine{
		{Age: "2m", Type: "Warning", Reason: "BackOff", Msg: "Back-off restarting failed container app in pod"},
		{Age: "5m", Type: "Warning", Reason: "Unhealthy", Msg: "Liveness probe failed: HTTP probe failed with statuscode 503"},
		{Age: "7m", Type: "Normal", Reason: "Pulled", Msg: "Container image already present on machine"},
		{Age: "9m", Type: "Normal", Reason: "Started", Msg: "Started container app"},
	}
	// browsing this pod's sessions: two timestamped dirs (one a snapshot bundle)
	m.caps = []capEntry{
		{Name: "20260705T103000Z", Size: 12 << 10, Mod: time.Now().Add(-3 * time.Minute), Dir: true},
		{Name: "20260705T091500Z", Size: 34 << 20, Mod: time.Now().Add(-2 * time.Hour), Dir: true, Snap: true},
	}
	m.capsCwd = "dumps/pods/app-debug-demo-app-6c6c4b5769-s9jdg"
	m.logs.lines = classifyLogs([]string{
		"10:29:51 INFO  [http-nio-8080-exec-3] c.e.d.DemoController : request served in 12ms",
		"10:29:53 INFO  [scheduler-1] c.e.d.CacheWarmer : warmed 1200 entries",
		"10:29:55 WARN  [HikariPool-1 housekeeper] com.zaxxer.hikari.pool.HikariPool : pool is near capacity",
		"10:29:57 INFO  [http-nio-8080-exec-7] c.e.d.DemoController : request served in 9ms",
		"10:30:01 ERROR [http-nio-8080-exec-2] o.a.c.c.C.[.[.[/].[dispatcherServlet] : Servlet.service() threw exception",
		"java.lang.OutOfMemoryError: Java heap space",
		"\tat com.example.debugdemo.LeakyService.grow(LeakyService.java:42)",
		"\tat com.example.debugdemo.DemoController.leak(DemoController.java:31)",
		"10:30:02 INFO  [http-nio-8080-exec-4] c.e.d.DemoController : request served in 14ms",
		"10:30:04 INFO  [scheduler-1] c.e.d.MetricsPusher : flushed 40 metrics",
		"10:30:06 INFO  [http-nio-8080-exec-1] c.e.d.DemoController : request served in 11ms",
		"10:30:08 INFO  [http-nio-8080-exec-5] c.e.d.DemoController : request served in 10ms",
	})
	m.logs.when = time.Now()
	m.pods = []string{
		"app-debug-demo-app-6c6c4b5769-s9jdg  Running  restarts=34",
		"app-debug-demo-app-6c6c4b5769-x7k2p  Running  restarts=2",
		"app-debug-demo-app-6c6c4b5769-q1r8n  Running  restarts=0",
	}
	m.podsScope = "selector"
	return m
}

func renderDemo(what string) string {
	m := demoModel()
	switch what {
	case "menu":
		m.scr = scMenu
		return m.menuView()
	case "dashboard":
		m.scr = scMenu
		m.width, m.height = 200, 50
		return m.menuView()
	case "compact":
		// narrow terminal: incident-checklist order, TARGET + NEXT on top
		m.scr = scMenu
		m.width = 90
		return m.menuView()
	case "focus":
		m.scr = scMenu
		m.width, m.height = 200, 50
		m.logs.focus = true
		return m.menuView()
	case "detail":
		m.scr = scDetail
		m.width, m.height = 120, 0 // height 0 → render all cards (no scroll window)
		return m.detailView()
	case "output":
		m.scr = scOutput
		m.width, m.height = 120, 40
		m.out = outState{title: "jdebug status", done: true, ok: true,
			raw: []byte("how to read this: STATUS should be Running; RESTARTS counts crashes\n\nNAME    READY   STATUS    RESTARTS   AGE\npod-a   1/1     Running   34         2d\n\nrecent events:\n5m  Warning  BackOff  pod/pod-a  Back-off restarting failed container")}
		m.rewrapOut()
		return m.outputView()
	case "runpane":
		// the dashboard with a finished command held in the bottom strip
		m.scr = scMenu
		m.width, m.height = 200, 50
		m.out = outState{title: "jdebug status", done: true, ok: true, show: true,
			raw: []byte("how to read this: STATUS should be Running; RESTARTS counts crashes\n\nNAME    READY   STATUS    RESTARTS   AGE\npod-a   1/1     Running   34         2d")}
		m.rewrapOut()
		return m.menuView()
	case "gate":
		m.t.Pod = ""
		m.remote = probe{OK: false, Cluster: true, When: time.Now().Add(time.Hour), Lines: []string{
			cSafe.Render("   ✓") + cMuted.Render(" cluster reachable"),
			cDisr.Render("   ✗") + cMuted.Render(" pod — none selected yet (press ") + cKey.Render("g") + cMuted.Render(", then ") + cKey.Render("p") + cMuted.Render(", and pick the exact pod)"),
			cFaint.Render("   · container — checked once a pod is selected"),
		}}
		return m.menuView()
	case "blocked":
		// a multi-blocked target: no selector, RBAC denial, no metrics, secured actuator
		m.scr = scBlocked
		m.width, m.height = 120, 0
		m.t.Selector = ""
		m.podsErr = "pods is forbidden: User \"dev\" cannot list resource \"pods\""
		m.panel.NoMetrics = true
		m.panel.ActuatorOK = false
		return m.blockedView()
	case "local":
		m.mode = 2
		return m.menuView()
	case "help":
		return m.helpView()
	case "chooser":
		return m.chooserView()
	case "editor":
		return m.editorView()
	case "wizard":
		m.scr = scWizard
		return m.wizardView()
	}
	return "unknown screen: " + what + " (menu|dashboard|compact|focus|output|runpane|gate|local|help|chooser|editor|wizard|blocked)"
}
