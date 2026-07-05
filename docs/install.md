---
title: Install
nav_order: 2
---

# Install

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
