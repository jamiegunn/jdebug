# jdebug — JVM debug kit for Kubernetes

**Full documentation lives in [`docs/`](docs/) — a GitHub Pages site** (after
pushing to GitHub: Settings → Pages → Source: *GitHub Actions*; the included
workflow builds and deploys it). Guides: install, getting started, command
reference, capture tiers, diagnosis playbooks, troubleshooting, testing,
roadmap.

Capture and analyze JVM diagnostics from a pod **without a JDK in the image**.
`jdebug` drives thread/heap capture, memory anatomy, an offline snapshot bundle,
log tailing, and runtime log-level changes against **any Spring Boot / JVM pod**,
over `kubectl`. It is self-contained — no assumptions about a particular app,
namespace, or cluster; it uses whatever `kubectl` context is active.

## Why

Production JVM images are often JRE-only (no `jstack`/`jmap`/`jcmd`), and you may
not be allowed to `kubectl debug` a JDK sidecar in. `jdebug` prefers the tools
that work anyway, and falls back only when it has to.

**Three capture tiers** (in preference order):
1. **actuator** (default) — Spring Boot's `/actuator/threaddump` + `/actuator/heapdump`. Works JRE-only, no binary needed.
2. **jattach** — a small static binary that speaks the Hotspot attach protocol, for the full `jcmd` surface (`GC.heap_info`, `VM.native_memory`, `JFR`, …). **Vendored in this repo** (pinned version, checksum-verified before every install — nothing is downloaded at runtime); no manual placement.
3. **jdk** — last resort: an ephemeral JDK container via `kubectl debug` for `jstack`/`jmap`.

## Install

```sh
./install.sh                 # symlink `jdebug` into ~/.local/bin
./install.sh --prefix ~/bin
./install.sh --uninstall
```
Or run it in place: `./jdebug <cmd>`. (The CLI resolves symlinks, so the
symlink install works from anywhere on PATH.)

The interactive menu and wizard are the Go (Bubble Tea) frontend. `jdebug`
uses a local dev build (`make tui`) when present, otherwise the **vendored,
hash-verified** binary under `vendor/tui/` for your platform. Vendored
platforms today: **darwin-arm64, linux-amd64, linux-arm64** — on an Intel Mac
(darwin-amd64) run `make tui` once (needs Go). Every CLI command works
without the TUI.

How the vendored binaries are kept honest, in three layers:

- **At runtime**, `jdebug` verifies each vendored binary against
  `vendor/tui/SHA256SUMS` before executing it — a tampered or corrupt binary is
  refused — and warns if the sources beside it no longer match the provenance
  recorded in `BUILDINFO`.
- **On push** (opt-in: `make hooks`), the pre-commit hook re-vendors and
  re-hashes whenever `tui/`/`core/` change, and the pre-push hook recomputes the
  source fingerprint and refuses to push if it doesn't match `BUILDINFO`.
- **In CI** (not skippable), a Linux `provenance` job re-runs that same check on
  every push, so the guarantee holds on a userland that isn't the author's.

The fingerprint recipe is locale-pinned (`LC_ALL=C`) so it yields the same hash
on every platform, and the builds are deterministic (`-trimpath`, stripped), so
anyone can rebuild `vendor/tui/` and reproduce the committed hashes byte-for-byte.

## Usage

```sh
jdebug -n <namespace> -l <selector> <command> [--container <name>]

jdebug doctor                                  # pre-incident checkup: host, cluster, target, actuator
jdebug health                                  # actuator health + per-subsystem
jdebug status                                  # pod status + events
jdebug top                                     # top pods + HPA
jdebug memory                                  # cgroup RSS vs JVM heap/non-heap, reconciled
jdebug metrics [name]                          # metric names, or one live value (trend by re-running)
jdebug threads   [--via actuator|jattach|jdk]  # thread dump (default: actuator)
jdebug heap      [--via actuator|jattach|jdk]  # heap dump — PAUSES the JVM (needs --confirm)
jdebug jcmd "GC.heap_info"                     # any jcmd via jattach
jdebug snapshot  [--heap]                      # offline bundle (metrics, threads, memory, jcmd)
jdebug dumps                                   # list captured evidence + how to analyze each
jdebug analyze                                 # first-pass triage of every capture (deadlocks, DOWN health, OOM risk)
jdebug logs -l app=payments                    # stream logs from all replicas (needs a selector)
jdebug log-level <logger> <LEVEL>              # runtime level change via actuator
jdebug install-jattach                         # pre-stage jattach in the pod

jdebug wizard                                  # guided, symptom-driven capture flow
jdebug                                         # interactive menu (opens with a mode chooser)
```

**Guided diagnosis.** New to the toolkit or the JVM? `jdebug wizard` (also `▶ w`
in the menu) asks what you're seeing — OOMKilled, slow/hung, high CPU, creeping
memory (leak), GC pauses, crash-looping, "a deploy just happened", or "not
sure" — then runs the right capture sequence for that symptom and names the
analyzer to open next. Destructive steps (heap dumps) ask first.

Every cluster-facing command takes `-n/--namespace`, `-l/--selector`,
`--container`, and `--help`. (`jdebug analyze` is the exception: it works on
local capture files, so it takes a path — `jdebug analyze [path|--deep|--diff]`.)

Captures (thread/heap dumps, snapshots) land under the kit's own `dumps/`
directory — git-ignored, one findable place regardless of where you ran the
command from. Override per run with `$OUT_DIR`, or move the root with
`$JDEBUG_DUMPS`.

## Target selection

Defaults come from flags, then env, then built-ins:

| | flag | env | default |
|---|---|---|---|
| namespace | `-n` | `JDEBUG_NAMESPACE` | `default` |
| selector | `-l` | `JDEBUG_SELECTOR` | *(any pod in the namespace)* |
| container | `--container` | `JDEBUG_CONTAINER` | `app` |
| kube context | — | `KUBECONFIG` / kubectl | ambient |

## jattach binary

**Vendored in this repo** at `vendor/jattach/` — a pinned version of
[jattach](https://github.com/jattach/jattach), one statically-linked binary per
arch (x64, arm64; static so the same binary runs on glibc *and* musl/alpine
pods). At install time jdebug matches the pod's arch (`uname -m`), **verifies
the binary against `vendor/jattach/SHA256SUMS`**, and `kubectl cp`'s it in; a
missing or checksum-failing file refuses to install. Nothing is downloaded at
runtime, so air-gapped clusters work out of the box. Build/provenance notes:
`vendor/jattach/PROVENANCE.md`.

To use your own build instead, pass `--binary /path/to/jattach` (or set
`$JATTACH_BINARY`) — an explicit override that bypasses the vendored copy and
its checksum gate.

## No kubectl inside the pod? (`jdebug-local`)

`jdebug` is an **operator-side** tool — it drives the pod from *outside* via
`kubectl exec`, so it needs kubectl + a kube context. When you only have a shell
*inside* the container (JRE-only image, no kubectl — e.g. `kubectl exec -it`, a
debug sidecar, or `nsenter` from the node), use **`jdebug-local`**: a single
POSIX-`sh` file that runs the same captures against `localhost:8080/actuator`,
`/tmp/jattach`, and `/proc` — nothing off-box.

Get it in:
```sh
jdebug push-local                     # kubectl cp it to <pod>:/tmp/jdebug-local
# or: kubectl cp jdebug-local <ns>/<pod>:/tmp/jdebug-local -c app
# or: just paste the file into the pod shell
```
Then, inside the pod:
```sh
sh /tmp/jdebug-local help             # comprehensive help; `help <cmd>` for detail
sh /tmp/jdebug-local memory
sh /tmp/jdebug-local threads > /tmp/threads.txt
sh /tmp/jdebug-local jcmd "GC.heap_info"      # needs jattach staged (jdebug install-jattach)
sh /tmp/jdebug-local snapshot --heap
```
The `jdk` tier isn't available in-pod (it needs `kubectl debug` from outside);
the actuator + jattach + memory tiers all work.

## Requirements

`kubectl` on your PATH (plus `python3` for `jdebug memory`), a
reachable kube context, and a pod that runs as the same uid your `kubectl exec`
lands as (jattach attaches same-uid). All actuator-backed commands use whatever
HTTP client is **in the pod** — `curl` or busybox `wget` — so they work against
a stock JRE-alpine image with nothing added.

Actuator endpoint exposure: **stock Spring Boot exposes only `/actuator/health`
over HTTP** — the capture endpoints need the app to opt in
(`management.endpoints.web.exposure.include=health,threaddump,heapdump,metrics,loggers`).
Without it, tier 1 404s and captures fall back to jattach/jdk automatically;
`jdebug doctor` probes `/threaddump` and names this when it's the blocker.

Secured actuators: if yours requires credentials, set
`ACTUATOR_AUTH=bearer:ENV_VAR` (or `basic:USER_VAR:PASS_VAR`) naming the pod's
OWN env vars — never a literal secret (menu: target editor `k`). Or skip HTTP
entirely and capture via the jattach tier (`--via jattach`), which needs no
actuator at all.

Heap dumps and `snapshot --heap` **pause the JVM** — they require `--confirm` and
should be treated as destructive in production.

## Tests

```sh
tests/run-tests.sh
```

Self-contained (no framework, no cluster): `tests/mocks/{kubectl,curl}` fake the
cluster and the in-pod HTTP, driven by `MOCK_*` env vars. The suite (~390
assertions) exercises the CLI, both tools' error messages, the confirm gates,
end-to-end mock captures (real bytes at the printed path, manifest recorded,
kubectl actually invoked), the TUI menus/wizard/help, the session log, and the
plain-language cluster diagnostics.

**What the suite does and doesn't prove:** it proves messages, gates, and
capture plumbing against a *mock* kubectl. The live-JVM suite
(`tests/live/`) and the kind/real-cluster suite (`tests/integration/`) prove
real transport and real JVM behavior, but they are **run manually** — the kind
suite has had one green run against a real k3s cluster and is not yet wired
into CI. See `docs/testing.md` and `docs/architecture.md` for the honest map.

## Known limits

- **Thread-dump analysis** reads jstack-style text and Spring actuator JSON.
  It detects monitor **and** `java.util.concurrent` (ReentrantLock) deadlock
  cycles, but the JDK-21 `jcmd Thread.dump_to_file` plain format is refused by
  name (not silently blessed), and **virtual threads never appear in
  actuator/ThreadMXBean dumps at all** — jdebug flags a dump as structurally
  incomplete when it sees signs of a virtual-thread app, but it cannot show
  you those threads.
- **Session logs record command output verbatim.** Secret redaction is still a
  roadmap item — treat `dumps/session-*.log` with the same care as the
  captures themselves.
- **Capture timeouts are opt-in** (`JDEBUG_TIMEOUT=90s`, v2 engine only);
  without it a wedged JVM or dropped connection can stall a capture until you
  Ctrl-C (which is handled cleanly).
- **TUI known gaps** (tracked in `docs/ux-followups.md`): cancelling a
  streaming command can leave the underlying kubectl running in the
  background, and some dashboard panel reads render as blank/zero rather than
  "UNKNOWN" when RBAC denies them.

## License

0BSD — do whatever you want with it; no attribution required, no warranty given.
