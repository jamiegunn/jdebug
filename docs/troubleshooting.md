---
title: Troubleshooting
nav_order: 10
---

# Troubleshooting

Every jdebug error is designed to explain itself — this page collects them
with extra context. First move for any environment problem: `jdebug doctor`.

## "can't reach the Kubernetes cluster"

jdebug probes the cluster before every command and translates the failure:

### …UP but REJECTED your credentials (Unauthorized)

The cluster answered and refused your token — it expired. Typical on managed
clusters (EKS/GKE/AKS/OpenShift) where SSO/OIDC/cloud-CLI logins time out.
Re-authenticate (`aws sso login`, `gcloud auth login`, `az login`, `oc login`)
and re-run. **Switching contexts will not fix expired credentials.**

### …TLS certificate isn't trusted

**Local clusters** (Rancher Desktop, k3s, minikube, kind, Docker Desktop): the
cluster was recreated and the old kubeconfig entry went stale.

- restart the local cluster app — most rewrite the kubeconfig on startup
- or switch to a working context: press `g` in the menu, or
  `kubectl config use-context <name>`

**Managed clusters** (EKS/GKE/AKS/OpenShift): usually a corporate proxy
intercepting TLS, a rotated cluster CA, or a stale kubeconfig — re-fetch it
(`aws eks update-kubeconfig`, `gcloud container clusters get-credentials`),
or ask your network team about the proxy's CA.

### …nothing answered at the cluster's address

The cluster is off, asleep, or unreachable: start it (Rancher/Docker
Desktop), connect the VPN for remote clusters, or switch context.

### …no context selected

`kubectl config use-context <name>` (list them with
`kubectl config get-contexts`), or point `KUBECONFIG` at the right file.

## "no pod matched namespace=… selector=…"

The target is wrong, not the cluster. Check the namespace (`-n`), the label
selector (`-l app=…` — find labels with `kubectl -n <ns> get pods --show-labels`),
or press `g` in the menu and use the pod picker.

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

- from outside a pod: `jdebug install-jattach` (copies in the vendored, checksum-verified binary — no download)
- in the menu's local modes: press `i`
- air-gapped: place a binary manually and pass `--binary /path`

If jattach runs but attach fails, check **uid parity**: the attach protocol
only works when the caller runs as the same user as the JVM.

## "no JVM found / nothing maps libjvm"

JVM discovery looks for a process named `java`, then for any process that
maps `libjvm` — which catches custom launchers (`jwebserver`, `jshell`,
jlink-built images). If neither matches, there is no HotSpot JVM in that
container/box; if you know better (exotic setups), point at it explicitly
with `JVM_PID=<pid>` (jdebug-local).

## "heap dump PAUSES the JVM … --confirm"

Not an error — the safety gate. Add `--confirm` when you accept that the app
will freeze for the duration of the dump.

## "capture looks wrong (no marker) / not a valid hprof"

The endpoint answered with something other than a real dump (an error page,
a truncated stream). The file is kept so you can look inside; the usual cause
is a secured or misrouted actuator — try `--via jattach`.

## "not found (HTTP 404)" from a working actuator — endpoints not exposed

Stock Spring Boot serves **only `/actuator/health`** over HTTP. `/threaddump`,
`/heapdump`, `/metrics`, and `/loggers` need the app to opt in:

```properties
management.endpoints.web.exposure.include=health,threaddump,heapdump,metrics,loggers
```

This is the most common tier-1 failure on real apps. Until the app exposes
them, captures auto-fall back to the jattach/jdk tiers; `jdebug doctor`
probes `/threaddump` and names this exact blocker.

## "no container named 'app'"

jdebug's default container name is `app`; real clusters usually name the
container after the service. Pass `--container <name>` (or set
`JDEBUG_CONTAINER`, or the menu's target editor `k`). List the real names:
`kubectl -n <ns> get pod <pod> -o jsonpath='{.spec.containers[*].name}'`.

## "the container has NO SHELL (a distroless/minimal image)"

The pod is (probably) fine — the image just ships no `sh`, so the in-pod
tiers (actuator fetch, jattach install) can't run. Use the ephemeral debug
container instead: `jdebug threads --via jdk` (needs the cluster to allow
ephemeral containers — `jdebug doctor` checks).

## "your RBAC doesn't allow …" (Forbidden)

The error names the exact permission. Ask your cluster admin for that verb
and nothing more (typically `get/list` on pods, events, `pods/log`, `create`
on `pods/exec` for captures, and `patch` on `pods/ephemeralcontainers` for
the jdk tier). Every other jdebug command keeps working.

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
