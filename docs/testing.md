---
title: Testing
nav_order: 12
---

# Testing

## Running the suite

```sh
tests/run-tests.sh
```

Self-contained: no framework, no cluster, no network. The suite (130+
assertions) finishes in seconds and exits non-zero on any failure — safe for
CI and pre-commit alike. GitHub Actions runs it on every push, on **both
Ubuntu and macOS**, because GNU/BSD userland differences (sed, tar, ls) have
produced real bugs here before.

## How it works

`tests/mocks/kubectl` and `tests/mocks/curl` sit first on `PATH` and are
scripted with environment variables, so every failure mode is reproducible
in milliseconds:

| variable | values | simulates |
|---|---|---|
| `MOCK_KUBECTL` | `ok` · `x509` · `refused` · `noctx` | cluster reachability outcomes |
| `MOCK_PODS` | `one` · `none` · `multi` | what the selector matches |
| `MOCK_EXEC_OUT` | any string | what an in-pod command prints |
| `MOCK_HTTP` | `ok` · `fail` | the pod's actuator (for `jdebug-local`) |
| `MOCK_LOG` | file path | records every kubectl invocation for assertions |

## What's covered — and the philosophy

The **user-facing text is the contract**. Tests assert that error messages
explain themselves ("TLS certificate isn't trusted… Rancher Desktop… the
fix"), that raw kubectl noise is suppressed, that every `--confirm` gate
holds, that warnings fire (`ROOT` at `TRACE` warns about volume), and that
the TUI keeps a failed command's output on screen — that last one is a
regression test for a real bug where failures skipped the pause and wiped
their own error.

Coverage map:

- syntax of every script (bash + POSIX sh)
- CLI basics, exit codes, unknown-input handling
- `check_cluster` translation of all three failure classes
- `doctor` healthy / unreachable / no-pods verdicts
- multi-pod announcement and listing
- `dumps` listings, analyzer hints, data-handling warning
- all heap-dump confirm gates (four entry points)
- `jdebug-local` end to end against mock HTTP: health, metrics, memory math,
  heap write + extraction hint, jcmd guidance, actuator-down messaging
- TUI: menus, glossary, wizard, session log, context/pod pickers, quick-picks
- `install.sh` symlink round-trip (including running through the symlink)

## Adding a test

Append to the relevant section of `tests/run-tests.sh`:

```bash
MOCK_KUBECTL=refused run_case ./jdebug health
assert_rc  "my case: exits 3" 3
assert_has "my case: says why" "nothing answered"
```

`run_case` captures stdout+stderr+exit code; `run_input` feeds stdin for TUI
flows; `assert_has` / `assert_not` / `assert_rc` do the checking. Mocks can
grow new branches — keep them dumb and env-driven.

## Manual verification drill

Mocks can't prove cluster reality. Before a release, against a disposable
cluster (kind/k3d) running any Spring Boot app:

1. `jdebug doctor` — all green
2. `jdebug threads` on a **wget-only** JRE-alpine image (tier 1 portability)
3. `jdebug heap --confirm --via jattach` (tier 2, uid parity)
4. `jdebug threads --via jdk` (tier 3, ephemeral containers)
5. `jdebug snapshot` and open the bundle
6. menu: `t` context switch, pod pin with 2+ replicas, `w` one full wizard flow
