---
title: Troubleshooting
nav_order: 10
---

# Troubleshooting

Every jdebug error is designed to explain itself — this page collects them
with extra context. First move for any environment problem: `jdebug doctor`.

## "can't reach the Kubernetes cluster"

jdebug probes the cluster before every command and translates the failure:

### …TLS certificate isn't trusted

Your kubeconfig's saved credentials don't match the cluster's current
certificate. Almost always: a local cluster (Rancher Desktop, k3s, minikube,
kind, Docker Desktop) was recreated and the old kubeconfig entry went stale.

- restart the local cluster app — most rewrite the kubeconfig on startup
- or switch to a working context: press `t` in the menu, or
  `kubectl config use-context <name>`

### …nothing answered at the cluster's address

The cluster is off, asleep, or unreachable: start it (Rancher/Docker
Desktop), connect the VPN for remote clusters, or switch context.

### …no context selected

`kubectl config use-context <name>` (list them with
`kubectl config get-contexts`), or point `KUBECONFIG` at the right file.

## "no pod matched namespace=… selector=…"

The target is wrong, not the cluster. Check the namespace (`-n`), the label
selector (`-l app=…` — find labels with `kubectl -n <ns> get pods --show-labels`),
or press `t` in the menu and use the pod picker.

## "actuator … unreachable / not answering"

The app isn't serving the actuator endpoints where jdebug is looking.

- **custom management port or base path** — `--actuator-base http://localhost:9001/manage`
  (or `$ACTUATOR_BASE`)
- **actuator secured** — the actuator tier can't authenticate; capture via
  the jattach tier instead (`--via jattach`), which needs no actuator
- **app too wedged to serve HTTP** — same: `--via jattach`, or tier 3
- **no actuator dependency at all** — everything still works through jattach

## "jattach not found / not staged"

The jattach tier needs its binary in the pod (`/tmp/jattach`) or on the box.

- from outside a pod: `jdebug install-jattach` (downloads, caches, copies in)
- in the menu's local modes: press `i`
- air-gapped: place a binary manually and pass `--binary /path`

If jattach runs but attach fails, check **uid parity**: the attach protocol
only works when the caller runs as the same user as the JVM.

## "heap dump PAUSES the JVM … --confirm"

Not an error — the safety gate. Add `--confirm` when you accept that the app
will freeze for the duration of the dump.

## "capture looks wrong (no marker) / not a valid hprof"

The endpoint answered with something other than a real dump (an error page,
a truncated stream). The file is kept so you can look inside; the usual cause
is a secured or misrouted actuator — try `--via jattach`.

## "logs needs a selector"

Streaming a whole namespace isn't supported by kubectl; pass `-l app=…`
(or set `JDEBUG_SELECTOR`).

## `jdebug memory` exits complaining about metrics

Deliberate: if the actuator metrics scrape fails mid-report, the report
refuses to print a table of zeros that would mislead an investigation.
Fix the actuator reachability (above) and re-run; RSS-only data is still
available via `jdebug-local memory` inside the pod.

## Something else

Check the session log (`dumps/session-*.log`) for the exact command and
output, run the printed `$ kubectl …` line by hand, and read
[Capture tiers](capture-tiers) for what each route requires.
