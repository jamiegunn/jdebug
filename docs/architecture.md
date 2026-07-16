# Architecture — v2 direction and migration status

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
| 2 | Port the capture verbs (`threads`/`heap`/`jcmd`/`snapshot`): implement the three tiers as `Acquirer`s, route from the bash dispatcher, verify with the frozen spec + existing suite. Retire `capture/*.sh`. | TODO |
| 3 | Port observe/analyze with real parsers (thread-dump lock graph → deadlock detection on every tier's format, fixing F4; manifest-driven `analyze --diff` for memory reports, fixing F9). Retire `observe/*.sh` piecewise. | TODO |
| 4 | TUI calls core directly (no bash shell-outs); `jdebug` becomes a thin exec of the binary; single-binary distribution with `go:embed`ed jattach + checksums. | TODO |
| 5 | Integration CI: kind cluster + Spring Boot fixture app (secured/unsecured actuator, OOM + deadlock endpoints), chaos cases (mid-`cp` kill, crash-loop, curl-less image) — the boundaries mocks can't test. | TODO |

## Rules while migrating

Every ported verb must pass the frozen spec diff and the existing black-box
suite before its bash implementation is deleted. The bash version of any
unported verb keeps working at every phase — the transition must be invisible
from the outside. New capabilities (e.g. crash-loop `fetch-heap`, F7) are
built in core, never added to the bash side.
