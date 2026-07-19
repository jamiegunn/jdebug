package main

// blocked.go — the blocked-by view ('b'). A check that can't run is shown as an
// operator STATE with the least-privilege permission, setup step, or fallback
// route that unblocks it — never a bare "unavailable" or a stack trace. This is
// the aggregation the bash side's explain_kubectl_error phrases one call at a
// time; here we read the live signals and list every currently-blocked check.

type blocker struct {
	state string // the operator state, e.g. "blocked by RBAC"
	why   string // one plain-language line
	fix   string // the least-privilege permission / setup / fallback route
}

// rbacBlocked reports whether any of the given fetch errors was a Forbidden
// reply (reusing the same matcher the kubectl enum uses).
func rbacBlocked(errs ...string) bool {
	for _, e := range errs {
		if e != "" && forbiddenRe.MatchString(e) {
			return true
		}
	}
	return false
}

// blockers reads the current signals and returns the checks that are blocked
// right now, most-fundamental first — a dead cluster hides everything below it,
// so we stop there rather than list a dozen downstream unknowns.
func (m model) blockers() []blocker {
	var b []blocker
	if m.mode == 1 {
		if m.remote.Unauthorized {
			// NOT unreachable: the cluster answered and rejected the token.
			// "switch context" is the wrong fix here — re-auth is.
			return []blocker{{"blocked by expired credentials",
				"the cluster is UP but rejected your token (expired SSO/OIDC/cloud-CLI login)",
				"re-authenticate: aws sso login · gcloud auth login · az login · oc login — then any key to re-probe"}}
		}
		if !m.remote.Cluster {
			return []blocker{{"blocked by cluster unreachable",
				"kubectl can't reach the cluster API, so nothing downstream can run",
				"press c for the full why + fix, or g to switch to a context that's up"}}
		}
		if m.t.Selector == "" {
			b = append(b, blocker{"blocked by no selector",
				"no label filter is set, so jdebug can't tell which pods are your app's",
				"press g → set a selector like app=NAME (the PODS pane suggests candidates)"})
		}
		if m.t.Pod == "" {
			b = append(b, blocker{"blocked by no pod",
				"captures target one specific pod, and none is pinned yet",
				"press g, then p, and pick the exact pod (e.g. the restarting one)"})
		}
		if rbacBlocked(m.podsErr, m.eventsErr, m.logs.err) {
			b = append(b, blocker{"blocked by RBAC (your kube permissions)",
				"your kube context is denied a read it needs (a Forbidden reply)",
				"ask for get/list on pods, events, and pods/log in this namespace — nothing more"})
		}
		if m.panel.NoMetrics {
			b = append(b, blocker{"blocked by missing metrics-server",
				"kubectl top has no data source, so live CPU/mem % is blank",
				"install metrics-server; requests/limits still show, and JVM heap still comes from actuator/jattach"})
		}
		if !m.panel.When.IsZero() && !m.panel.ActuatorOK {
			b = append(b, blocker{"blocked by no actuator (the app's health/metrics HTTP endpoint)",
				"the app's health URL didn't answer (secured, wrong path, or not exposed)",
				"if it's secured set auth with k; check the URL in g; or capture with no HTTP via jattach (t → jattach)"})
		}
	} else {
		// local / in-pod modes: the two routes are actuator and jattach
		switch {
		case !m.local.OK:
			b = append(b, blocker{"blocked by no route",
				"neither the actuator HTTP endpoint nor jattach is available in this pod",
				"press s to fix the actuator URL, or i to stage jattach (~80 KB, needs no HTTP)"})
		case !m.local.Jattach:
			b = append(b, blocker{"jattach not staged",
				"the no-HTTP JVM route isn't installed, so actuator-less captures can't run",
				"press i to stage jattach (~80 KB) — it talks to the JVM directly"})
		}
	}
	return b
}

// blockedView renders the blocked-by overlay, dismissed by any key.
func (m model) blockedView() string {
	out := "\n  " + cTitle.Render("blocked-by — what can't run right now, and what unblocks it") + "\n"
	bs := m.blockers()
	if len(bs) == 0 {
		out += "\n    " + cOK.Render("✓ nothing is blocked") + cMuted.Render(" — every check has a working route.") + "\n"
		out += "    " + cMuted.Render("if a command still fails, press ") + cKey.Render(".") + cMuted.Render(" on its row for the deps it needs.") + "\n"
	} else {
		for _, bl := range bs {
			out += "\n    " + cDisr.Render("✗ "+bl.state) + "\n"
			out += "      " + cFaint.Render("why  ") + cBody.Render(bl.why) + "\n"
			out += "      " + cFaint.Render("fix  ") + cOK.Render(bl.fix) + "\n"
		}
	}
	out += "\n  " + cFaint.Render("any key for the menu") + " "
	return out
}
