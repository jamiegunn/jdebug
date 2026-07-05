package main

// help.go — the glossary + workflow + safety rules screen ('?').

func (m model) helpView() string {
	h := func(s string) string { return "\n  " + cTitle.Render(s) + "\n" }
	li := func(term, def string) string { return "    " + cBody.Render(term) + "  " + cMuted.Render(def) + "\n" }
	return h("jdebug help — the words, the workflow, the safety rules") +
		h("THE WORDS") +
		li("pod         ", "one running copy of the app; replicas = several pods") +
		li("namespace   ", "a folder for pods; your app lives in one") +
		li("selector    ", "a label filter like app=payments that picks YOUR app's pods") +
		li("container   ", "pods can hold several; we talk to the app's one (usually \"app\")") +
		li("actuator    ", "Spring Boot's admin endpoints over HTTP — the safest way in") +
		li("thread dump ", "what every thread is doing — THE tool for slow/hung/high-CPU; safe") +
		li("heap dump   ", "every object in memory → a file — THE tool for leaks/OOM; PAUSES the app") +
		li("jattach     ", "~80 KB helper placed in the pod to talk to the JVM directly; jcmd rides it") +
		li("heap vs RSS ", "heap = what the JVM manages; RSS = what the container really uses") +
		h("A GOOD FIRST 10 MINUTES") +
		li("w wizard    ", cOK.Render("NOT SURE? START HERE.")+" tell it the symptom; it runs the right captures") +
		li("s status    ", "anything restarting or stuck? read the hints under the output") +
		li("h health    ", "a DOWN dependency? chase that system first") +
		li("d / a       ", "see what you captured / analyze it all in one pass") +
		h("KEYS NOT SHOWN ON THE MENU") +
		li(".           ", "what does each command do? source, risk, deps (or right-click a row)") +
		li("b           ", "what's blocked right now + how to unblock it (RBAC, metrics, actuator…)") +
		li("r / z       ", "refresh now / cycle background refresh (auto → quiet → paused)") +
		li("i           ", "stage jattach in the pod") +
		li("p           ", "push the in-pod tool (jdebug-local)") +
		li("g / M       ", "target editor / switch mode") +
		h("TRENDS + WHAT THE SCREEN DOES WHILE IDLE") +
		li("trends      ", "point-in-time samples (not averages), one per 20s: mem=% of limit, cpu=vs limit, ▲=a restart") +
		li("gaps        ", "a blank in a sparkline = that sample had no metric (metrics-server, or unknown)") +
		li("auto (live) ", "logs every 5s · pod/top/hpa/events every 20s · actuator heap every 20s (touches the app)") +
		li("z quiet     ", "stops log polling + the JVM/actuator probe; cheap kubectl reads stay (last heap held)") +
		li("z paused    ", "nothing runs on its own; r does one full refresh when you want it") +
		h("THE SAFETY RULES") +
		"    " + cMuted.Render("· most actions are read-only. The ones that CHANGE things ask first:") + "\n" +
		"        " + cDisr.Render("H heap / x --heap") + cMuted.Render("  PAUSE the JVM while they write (H asks for a second H)") + "\n" +
		"        " + cDisr.Render("R re-roll") + cMuted.Render("          rolling-restarts every pod in the deployment (second R)") + "\n" +
		"        " + cDisr.Render("K kill pod") + cMuted.Render("         deletes this pod; a managed one respawns (second K)") + "\n" +
		"        " + cCaut.Render("v verbosity") + cMuted.Render("        changes log volume live on every replica") + "\n" +
		"    " + cMuted.Render("· anything risky asks you first — cancelling is always safe") + "\n" +
		"    " + cMuted.Render("· every capture is saved under dumps/ + the session log — you can't lose evidence") + "\n" +
		"    " + cMuted.Render("· heap dumps + logs can contain real user data: treat them like production data") + "\n" +
		"\n  " + cFaint.Render("any key for the menu") + " "
}
