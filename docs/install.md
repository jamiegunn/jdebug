---
title: Install
nav_order: 2
---

# Install

These instructions are for **macOS**. Windows may work through WSL or another
Unix-like shell, but this kit and its install/build commands are not verified
as native Windows PowerShell/CMD steps.

```sh
./install.sh                 # symlinks `jdebug` into ~/.local/bin
./install.sh --prefix ~/bin  # or a bin dir of your choice
./install.sh --uninstall     # removes the symlink
```

Or run it in place — `./jdebug <cmd>` works from a checkout. The CLI resolves
symlinks to find the rest of the kit, so the symlink install works from
anywhere on PATH.

## Requirements

| What | Where | Why |
|---|---|---|
| `kubectl` + a reachable context | your machine | everything remote goes through it |
| `curl` | your machine | downloads the jattach helper (once, then cached) |
| `python3` | your machine | only `jdebug memory` needs it |
| `curl` **or** busybox `wget` | in the pod | the actuator tier uses whichever exists — a stock JRE-alpine image works untouched |
| same uid as the JVM | your `kubectl exec` | the jattach tier attaches same-uid only |

## Build the optional Go TUI on a new Mac

The shell CLI runs directly from the checkout. The nicer Bubble Tea TUI is a
Go binary; build it once and `jdebug` will prefer it automatically.

Install macOS build prerequisites:

```sh
xcode-select --install       # installs make, git, compiler tools
brew install go kubectl python
```

Then build from the repo root:

```sh
make tui                     # writes tui/jdebug-tui
./jdebug                     # opens the menu; prefers the built TUI
```

If Homebrew is not installed, install Go and kubectl by your normal Mac
software-management path; the important pieces for `make tui` are `make` and
`go`.

If you copied the repo in a way that stripped executable bits, repair them:

```sh
chmod +x install.sh jdebug jdebug-local
chmod +x capture/*.sh observe/*.sh ui/*.sh tests/run-tests.sh
```

The repository should already have those modes when cloned with `git`; the
`chmod` commands are only a fix-up for zip/copy/fileshare transfers.

To verify the checkout and build:

```sh
make tui
tests/run-tests.sh
jdebug doctor
```

## Verify before you need it

```sh
jdebug doctor
```

`doctor` checks the host tools, the captures directory, the jattach cache,
cluster reachability, that pods match your target, and that the actuator
answers inside the pod — each with a ✓/!/✗ and a fix. Run it when you set up,
and again before an incident call. It exits non-zero if anything blocking is
wrong, so you can put it in a runbook or a cron.

## Picking your target

Defaults come from flags, then environment, then built-ins:

| | flag | env | default |
|---|---|---|---|
| namespace | `-n` | `JDEBUG_NAMESPACE` | `default` |
| selector | `-l` | `JDEBUG_SELECTOR` | *(any pod in the namespace)* |
| container | `--container` | `JDEBUG_CONTAINER` | `app` |
| actuator URL | `--actuator-base` | `ACTUATOR_BASE` | `http://localhost:8080/actuator` |
| kube context | — | `KUBECONFIG` / kubectl | ambient |

jdebug never rewrites your kubeconfig. In the menu, press `t` to switch
contexts (with confirmation), set the namespace/selector, and pin a specific
pod when several match.
