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
| `ACTUATOR_AUTH` / `JDEBUG_ACTUATOR_AUTH` | — | *(none)* | secured-actuator credentials as a REFERENCE to the pod's own env vars: `bearer:ENV_VAR` or `basic:USER_VAR:PASS_VAR` — never a literal secret. (The menu's target editor `k` sets the same thing.) |
| `KUBECONFIG` | — | ambient | standard kubectl context selection; never rewritten |

> **Stock Spring Boot note:** only `/actuator/health` is exposed over HTTP by
> default. For the capture endpoints the app must opt in, e.g.
> `management.endpoints.web.exposure.include=health,threaddump,heapdump,metrics,loggers`.
> `jdebug doctor` probes `/threaddump` specifically and tells you when this is
> the blocker.

## Capture & evidence

| variable | default | meaning |
|---|---|---|
| `JDEBUG_DUMPS` | `<kit>/dumps` | root for all operator-side captures + session logs |
| `OUT_DIR` | per command | one-off override of a capture's output dir |
| `JATTACH_VENDOR_DIR` | `<kit>/vendor/jattach` | the vendored, checksum-verified jattach binaries |
| `JDEBUG_TIMEOUT` | *(none = no limit)* | global budget for one v2-engine capture, e.g. `90s`, `5m` — so no capture can hang an incident call. Unset by default: multi-GB heap dumps legitimately take minutes |
| `JDEBUG_V1` | *(unset)* | `1` forces the v1 bash capture tiers even when the v2 Go engine is present |

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
