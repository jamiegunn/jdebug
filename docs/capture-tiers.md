---
title: Capture tiers
nav_order: 7
---

# The three capture tiers

The same evidence — thread dumps, heap dumps, JVM internals — can be captured
three ways, and jdebug always prefers the least invasive one that works.

## Tier 1 — actuator (default)

Ask the app itself: Spring Boot's `/actuator/threaddump` and
`/actuator/heapdump` over HTTP, executed **inside** the pod with whatever
HTTP client the image has (curl, or busybox wget — both stock-alpine safe).

- **Needs:** the app serving HTTP with the actuator **capture endpoints
  exposed** — stock Spring Boot exposes only `/health`; the app must opt in:
  `management.endpoints.web.exposure.include=health,threaddump,heapdump,metrics,loggers`.
  This is the most common tier-1 failure on real apps; `jdebug doctor` probes
  `/threaddump` and names it.
- **Doesn't need:** anything installed, any binary, any special permission.
- **Fails when:** the endpoints aren't exposed (above), the app is too wedged
  to serve HTTP, actuator is absent, on a custom port (`--actuator-base`), or
  secured (`ACTUATOR_AUTH` — see configuration).

## Tier 2 — jattach

A small statically-linked binary that speaks the JVM's attach protocol
directly — no actuator, no JDK. The binary is **vendored in this repo**
(`vendor/jattach/`, pinned version, one static build per arch — nothing is
downloaded at runtime). jdebug matches the pod's arch, **verifies the binary
against `vendor/jattach/SHA256SUMS`**, `kubectl cp`s it in, verifies it runs,
and finds the real java PID from `/proc` (PID 1 is the pause sandbox under
`shareProcessNamespace`).

- **Gets you:** the whole `jcmd` surface — `GC.heap_info`,
  `VM.native_memory`, `VM.flags`, `JFR.start`, heap and thread dumps.
- **Needs:** `kubectl exec` landing as the **same uid** as the JVM (the
  attach protocol requires it), and `/tmp` writable in the container.
- **Air-gapped:** works out of the box — the binary ships in the repo, so
  no network is needed. To use your own build instead, pass `--binary /path`
  (or `$JATTACH_BINARY`); that explicit override bypasses the checksum gate.
- **Pre-stage before an incident:** `jdebug install-jattach`.

## Tier 3 — jdk (last resort)

`kubectl debug` attaches a temporary JDK container (default
`eclipse-temurin:21-jdk-alpine`, override `$JDK_DEBUG_IMAGE`) that shares the
pod's PID namespace, hand-shakes the HotSpot attach protocol across the
container boundary via `/proc/<pid>/root`, and runs real `jstack`/`jmap`.

- **Needs:** cluster policy allowing ephemeral containers; a pullable (or
  pre-imported) JDK image; root in the debug container.
- **When you'd force it:** a wedged JVM that needs `jstack -F`, or policy
  that forbids placing binaries in pods but allows debug containers.

## Auto-degrade

`jdebug threads` / `jdebug heap` with no `--via` runs tier 1, and on failure
announces and tries tier 2, then tier 3. Each failure message names the next
tier and the exact command to force it. `--via <tier>` runs exactly one.

## Choosing manually

| situation | use |
|---|---|
| Spring Boot app with capture endpoints exposed | default (auto) — tier 1 will just work |
| stock Spring Boot (only `/health` exposed) | default (auto) — tier 1 404s and auto-falls back to tier 2 |
| actuator secured or absent | `--via jattach` |
| app not serving HTTP at all | `--via jattach` |
| need `VM.native_memory`, JFR, VM.flags | `jcmd` (always jattach) |
| JVM completely wedged | `--via jdk` (real jstack -F) |
| shell inside the pod, no kubectl | [`jdebug-local`](jdebug-local) |
