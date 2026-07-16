---
title: Development
nav_order: 13
---

# Development

## Repo layout

```
jdebug            the CLI front door: dispatch, tier auto-degrade, preflight, doctor, dumps
jdebug-local      single-file POSIX-sh in-pod/bare-metal tool (busybox ash compatible)
lib/common.sh     shared: target parsing, pod resolution, pod_fetch (curl-or-wget),
                  check_cluster, cache + dumps locations
capture/          the three tiers: actuator.sh · jattach.sh · jdk-threads.sh · jdk-heap.sh
observe/          memory-report.sh · snapshot.sh · tail-logs.sh · set-log-level.sh
githooks/         pre-commit (vendors the TUI + hashes), pre-push (verifies) — make hooks
tui/              Go (Bubble Tea) frontend — same keys/screens, shells out to
                  the bash CLI; `make tui` builds tui/jdebug-tui, which
                  `jdebug` prefers a dev build (tui/jdebug-tui), else the
                  vendored hash-verified binary (vendor/tui/, see make hooks)
install.sh        PATH symlink install/uninstall
tests/            run-tests.sh + mocks/{kubectl,curl} + pty-drive.py
docs/             this site (Jekyll, GitHub Pages)
```

The Go frontend is a thin renderer: all capture logic, error translation, and
safety gates live in the bash CLI it drives. Its suite runs inside
`tests/run-tests.sh` when a Go toolchain is present — `go vet`, update-logic
unit tests, `-render` parity assertions against the bash screens, and a full
interactive round-trip on a real pty (`tests/pty-drive.py`).

## Conventions that matter

- **Help lives in the header.** Each script's `--help` is its own header
  comment block (`usage()` prints it) — docs and code can't drift apart.
- **bash 3.2 compatible** (stock macOS): the `${arr[@]+"${arr[@]}"}` guard on
  every possibly-empty array expansion is load-bearing, not style.
- **`jdebug-local` stays one POSIX file.** No bashisms, no sourcing, nothing
  off-box. Its duplication of capture logic is deliberate — its value is
  being paste-able into a busybox shell.
- **BSD + GNU userland.** sed has no `\|` on macOS; tar/ls flags differ.
  The CI matrix tests both; prefer POSIX constructs.
- **Errors must explain themselves**: why it happened, then the fix, then the
  command to run. Raw tool noise gets translated (see `check_cluster`).
- **Nothing destructive without `--confirm`** at the script level *and* a
  y/N prompt at the menu level. Every capture validates its output.
- **Show the work**: `show_cmd` prints the real kubectl line before running it.

## Docs site

`docs/` is a Jekyll site (just-the-docs theme) published by
`.github/workflows/pages.yml`. One-time setup after pushing to GitHub:
**Settings → Pages → Source: GitHub Actions**. Preview locally with
`cd docs && bundle exec jekyll serve` if you have a Jekyll toolchain, or just
read the Markdown.

## Release checklist

1. `tests/run-tests.sh` green locally; CI green on both OSes
2. the [manual drill](testing#manual-verification-drill) against a disposable cluster
3. bump `JDEBUG_VERSION` in `jdebug`
4. update docs pages touched by the change
5. tag: `git tag vX.Y.Z && git push --tags`
