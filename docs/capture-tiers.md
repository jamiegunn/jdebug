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

- **Needs:** the app serving HTTP with actuator endpoints exposed.
- **Doesn't need:** anything installed, any binary, any special permission.
- **Fails when:** the app is too wedged to serve HTTP, actuator is absent,
  on a custom port (`--actuator-base`), or secured.

## Tier 2 — jattach

An ~80 KB statically-linked binary that speaks the JVM's attach protocol
directly — no actuator, no JDK. jdebug downloads it (host-side, arch-matched,
cached in `~/.cache/jdebug/`), `kubectl cp`s it in, verifies it runs, and
finds the real java PID from `/proc` (PID 1 is the pause sandbox under
`shareProcessNamespace`).

- **Gets you:** the whole `jcmd` surface — `GC.heap_info`,
  `VM.native_memory`, `VM.flags`, `JFR.start`, heap and thread dumps.
- **Needs:** `kubectl exec` landing as the **same uid** as the JVM (the
  attach protocol requires it), and `/tmp` writable in the container.
- **Air-gapped:** pre-place a binary and pass `--binary /path` (or
  `$JATTACH_BINARY`); pin the version with `$JATTACH_VERSION`.
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
| normal Spring Boot app | default (auto) — tier 1 will just work |
| actuator secured or absent | `--via jattach` |
| app not serving HTTP at all | `--via jattach` |
| need `VM.native_memory`, JFR, VM.flags | `jcmd` (always jattach) |
| JVM completely wedged | `--via jdk` (real jstack -F) |
| shell inside the pod, no kubectl | [`jdebug-local`](jdebug-local) |
