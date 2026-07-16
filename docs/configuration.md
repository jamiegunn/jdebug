---
title: Configuration
nav_order: 11
---

# Configuration

Everything is a flag or an environment variable, with one remembered layer:
the menu's target editor saves its selections to
`~/.config/jdebug/target` (respects `$XDG_CONFIG_HOME`; override the dir with
`$JDEBUG_CONFIG_DIR`), so namespace, selector, container, actuator URL, and
the pinned pod survive between sessions. Precedence is
**flags → environment → saved target → built-in defaults**. Change values in
the menu — or delete the file — to forget. A remembered pod pin that no
longer exists is detected at startup and falls back to auto with a notice.

## Targeting

| variable | flag | default | meaning |
|---|---|---|---|
| `JDEBUG_NAMESPACE` | `-n` | `default` | namespace |
| `JDEBUG_SELECTOR` | `-l` | *(empty = any pod)* | label selector for your app |
| `JDEBUG_CONTAINER` | `--container` | `app` | app container name in the pod |
| `ACTUATOR_BASE` | `--actuator-base` | `http://localhost:8080/actuator` | actuator URL *inside* the pod |
| `KUBECONFIG` | — | ambient | standard kubectl context selection; never rewritten |

## Capture & evidence

| variable | default | meaning |
|---|---|---|
| `JDEBUG_DUMPS` | `<kit>/dumps` | root for all operator-side captures + session logs |
| `OUT_DIR` | per command | one-off override of a capture's output dir |
| `JATTACH_VENDOR_DIR` | `<kit>/vendor/jattach` | the vendored, checksum-verified jattach binaries |

## jattach tier

| variable | flag | default | meaning |
|---|---|---|---|
| `JATTACH_BINARY` | `--binary` | — | use this local binary instead of the vendored one (bypasses the checksum gate) |
| `JATTACH_VERSION` | — | `v2.2` | the pinned version the vendored binaries were built from (informational) |
| `JATTACH_REMOTE_PATH` | — | `/tmp/jattach` | where it lands in the pod |

## jdk tier

| variable | default | meaning |
|---|---|---|
| `JDEBUG_JDK_IMAGE` | `eclipse-temurin:21-jdk-alpine` | image for the ephemeral debug container |

## In-pod (`jdebug-local`)

| variable | default | meaning |
|---|---|---|
| `ACTUATOR_BASE` | `http://localhost:8080/actuator` | also `-a` |
| `JATTACH_BIN` | `/tmp/jattach` | also `--jattach-bin` |
| `JVM_PID` | auto-detected | set when several JVMs share the box |
| `OUT_DIR` | `/tmp` | dumps and bundles |

## Presentation

| variable | meaning |
|---|---|
| `NO_COLOR` | disable all color (also auto-disabled when output isn't a terminal) |
| `JDEBUG_QUIET` | suppress the target-announcement banner |
| `JDEBUG_MODE` | `1/2/3` — skip the menu's mode question |

## A per-app profile, today

Until profiles exist, a shell alias does the job:

```sh
alias jdebug-payments='JDEBUG_NAMESPACE=payments JDEBUG_SELECTOR=app=payments jdebug'
```
