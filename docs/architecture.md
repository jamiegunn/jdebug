---
title: Architecture & migration status
nav_order: 14
---

# Architecture — v2 direction and migration status

## What jdebug is, in one sentence

**jdebug points a stressed on-call engineer at the right JVM in Kubernetes and
runs the safe diagnostic — capture-first, destructive-actions-gated — without
making them remember `jcmd` from `jmap`.** Everything else is in service of that
one job.

## The pieces — and which one to debug when it breaks

Five artifacts, but the split is deliberate (a strangler migration from bash to a
Go core, tracked in the ledger below), not accident. When something misbehaves,
this table says where to look and in what language:

| artifact | language | its job | you debug it when… |
|---|---|---|---|
| `jdebug` | bash | the dispatcher: parse verb + `--via`/target flags, route to a tier or the core | a verb routes wrong, a flag isn't parsed, the tier cascade misbehaves |
| `core/` → `jdebug-core` | Go (stdlib-only) | the v2 engine: cluster boundary, Target→Resolved→Confirmed, capture pipeline + validators, evidence manifest | a capture validates wrong, provenance is off, a ported verb misbehaves with `JDEBUG_V1` unset |
| `tui/` → `jdebug-tui` | Go (Bubble Tea) | the only interactive frontend: dashboard, wizard, heap reader | anything visual/interactive, click geometry, the `-analyze-heap` histogram |
| `jdebug-local` | POSIX sh | the in-pod / bare-metal / SSH tool: one file, no kubectl, actuator + jattach + `/proc` | a capture run *inside* a pod or over SSH, the native-jcmd-vs-jattach route |
| `vendor/jattach` | C (vendored, pinned, checksummed) | the tiny attach binary for the jcmd/threads/heap surface | attach fails; almost always a uid/policy issue, not jattach itself |

Rule of thumb: **interactive → `tui/`; a capture's correctness → `core/`; routing
and flags → `jdebug`; inside a pod or over SSH → `jdebug-local`.** The bash
`capture/*.sh` are the `JDEBUG_V1` safety net for verbs not yet ported to the
core; they are retired verb-by-verb (see the ledger), never in a big bang.

---

This document records the re-architecture decided after the adversarial
review (see the review's finding numbers referenced below): consolidate four
implementations into **one Go core with thin frontends**, shelling out to
kubectl behind an interface, and make the review's findings *unrepresentable*
rather than merely fixed. The product decisions — the capture tiers, the
confirm gates, the plain-language explanations, dumps-stay-local — are the
spec and do not change.

## Shape

    jdebug (bash dispatcher)          during migration: routes verbs
      ├── capture/*.sh observe/*.sh   v1 implementations (retire verb-by-verb)
      ├── tui/jdebug-tui              Go TUI (the only interactive frontend)
      │     └── core/ (Phase 2+)      calls the core directly, not bash
      └── core/                       the v2 engine (Go, stdlib-only module)
            cluster.go                ONE boundary to Kubernetes (kubectl shell-out;
                                      ambient kubeconfig, never rewritten; client-go
                                      adoptable later per-capability via this interface)
            target.go                 Target → Resolved → Confirmed: destructive ops
                                      only accept Confirmed, which cannot exist for an
                                      ambiguous match (F8 as a type, not an env var)
            capture.go                acquire → validate → store pipeline: tiers only
                                      implement acquire; validation is unskippable
                                      (F1/F5 impossible by construction) — hprof magic
                                      + size reconciliation, thread-dump marker
            manifest.go               evidence store: provenance in manifest.json
                                      (tier, command, bytes, sha256, verdict), not in
                                      filename conventions; sessions owner-only 0700

## Why these choices

Go: already in the repo (TUI + hprof parser), cross-compiles static binaries,
kills the bash-3.2/BSD-sed portability tax and the config-sourcing hazard
class. kubectl shell-out: inheriting the operator's ambient auth (exec
plugins, OIDC, contexts) is jdebug's superpower; reimplementing it through
client-go is unnecessary risk. The `Cluster` interface keeps that decision
reversible per-feature. The core is a separate stdlib-only module so it
builds and tests with no network and no version coupling to the TUI's deps.

## Migration ledger (strangler, verb-by-verb — never a big bang)

| Phase | What | Status |
|---|---|---|
| 0 | Freeze the parity spec: `scripts/freeze-spec.sh` → `spec/*.golden` (help text, verb list, all suite assertions). Parity during migration = an empty (or deliberate) diff against these. | **DONE** |
| 0b | Bash TUI removed; Go TUI is the only frontend (`-start wizard` added for `jdebug wizard`). Vendored, hash-verified TUI binaries in `vendor/tui/` kept fresh by the git hooks (`make hooks`; pre-commit builds + hashes, pre-push refuses stale). | **DONE** |
| 1 | Core foundation: `core/` module — Cluster interface, Target/Resolved/Confirmed, capture pipeline with mandatory validators, manifest store. Unit-tested (fake cluster; truncation, login-page, ambiguity, half-write cases). Not yet load-bearing. | **DONE (scaffold)** |
| 2 | Port the capture verbs: the three tiers as `Acquirer`s (`core/acquirers.go`); `threads`/`heap`/`jcmd` route to `core/jdebug-core` when built (`make core`; `JDEBUG_V1=1` forces bash). Suite green through both paths. Sessions carry `manifest.json` provenance. | **DONE** |
| 2b | `snapshot` ported (`core/snapshot.go`): core-native capture sections + per-section manifest verdicts (a failed section is honest in-file AND in the manifest); observe reporters still shell to bash (retire with Phase 3). Verified section-for-section identical to v1 against the mock cluster. **`capture/*.sh` retirement deliberately deferred**: the Go tiers have now had a first real-cluster run (Phase 5's kind job, green against k3s v1.31), but retirement stays gated until that job runs in CI across more images/arches — until then `capture/*.sh` are the `JDEBUG_V1` safety net. | **DONE** (retirement gated on kind-in-CI) |
| 3 | Real parsers behind `analyze`: thread-dump lock graph (`core/threaddump.go` — text + actuator-JSON formats; deadlocks found structurally, **fixing F4**: banner-less actuator dumps no longer get an all-clear) and memory-report diffing (`core/memdiff.go`, `jdebug analyze --diff <before> <after>`, **fixing F9**). analyze.sh resolves the core the same way capture does (local build OR the checksum-verified vendored binary), so a fresh clone gets the real parser; the grep fallback runs only when no core exists at all, and it refuses (rather than blesses) files it parses zero threads from. Remaining: retire `observe/*.sh` piecewise. | **DONE (parsers)** |
| 4 | TUI calls core directly (no bash shell-outs); `jdebug` becomes a thin exec of the binary; single-binary distribution with `go:embed`ed jattach + checksums. | TODO |
| 5 | Integration CI, two layers. **Live-JVM** (`tests/live/`): a pure-JDK fixture (`tests/fixture/DebugFixture.java` — real jcmd thread dumps, real hprofs, on-demand deadlock) + a kubectl shim = "real JVM, fake transport". **Green**: actuator + fetch-heap tiers capture genuine artifacts, the analyzer catches a genuine deadlock. The jattach tier runs here only on a **Linux** host (the vendored binary is a Linux ELF and the attach path needs `/proc`); on macOS it is **skipped** with a pointer to the kind layer (12 pass / 1 skip on macOS, all pass on Linux). **Kind/real-cluster** (`tests/integration/run-kind-tests.sh` + `.github/workflows/integration.yml`): real transport — exec/cp over an API server, the jattach tier install→attach→size-verified cp, the jdk tier's ephemeral-container attach, crash-loop behavior, deadlock end-to-end. **First real run green (9/9)** against a real k3s v1.31 cluster (2026-07): the F1 heap path produced an 11 MB size-verified hprof over real `kubectl cp`, the jdk ephemeral-container attach succeeded, the crash-looping pod failed loudly. The workflow lives at `.github/workflows/integration.yml`, currently **manual-only** (`workflow_dispatch`) — push/PR/nightly triggers are enabled once the kind job has run green across more images/arches. `capture/*.sh` remain the `JDEBUG_V1` safety net until the kind job runs in CI across more images/arches. | **DONE (live green; kind's first real run green, not yet in CI)** |

## Rules while migrating

Every ported verb must pass the frozen spec diff and the existing black-box
suite before its bash implementation is deleted. The bash version of any
unported verb keeps working at every phase — the transition must be invisible
from the outside. New capabilities are built in core, never added to the bash
side — done for `fetch-heap` (F7 closed): on-crash dump discovery
(HeapDumpPath from the pod spec + conventional dirs), size-verified retrieval
through the pipeline, and setup guidance when the hunt is empty
(`core/fetchheap.go`, live-validated against a real JVM-written hprof).
