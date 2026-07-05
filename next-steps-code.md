# Next Steps: Reduce Runtime Dependencies by Moving JSON Logic to Go

## Recommendation

Reduce the app's host-side runtime dependencies by moving JSON-heavy Kubernetes and JVM parsing logic out of shell scripts that call `python3`, and into Go over time.

Python is useful today as a small JSON parser inside Bash, but it is not fundamental to the product. As `jdebug` grows into a richer operator dashboard, Go is the better long-term home for topology, runtime context, security, and structured diagnostic logic.

## Why Change

Current shell scripts use `python3` mainly because Kubernetes returns nested JSON and Bash is a poor JSON parser. This is practical, but it means a Mac that can build/run the Go TUI still needs Python for the full diagnostic experience.

Reducing that dependency would make the tool easier to move to a fresh machine:

- Fewer host prerequisites.
- More consistent behavior across machines.
- Stronger typed parsing for Kubernetes objects.
- Easier unit testing without shell quoting issues.
- Better reuse between the TUI and CLI.
- Cleaner support for future runtime context/app wiring features.

## Current Python Usage To Target

Python is currently used for JSON parsing and small calculations in areas like:

- `observe/memory-report.sh`
- `observe/why.sh`
- `observe/security.sh`
- `observe/topology.sh`
- `observe/lifecycle.sh`
- selector discovery / target editing support
- test pty driver: `tests/pty-drive.py`

The test driver can stay Python for now. The priority is reducing Python from the operator's runtime path.

## Prefer Go For New Structured Logic

Do not add large new Python blocks for future features unless there is a strong reason.

New structured features should prefer Go, especially:

- Runtime context / app wiring.
- Workload topology sections.
- Services, ports, endpoints, ingress/gateway discovery.
- Environment, ConfigMap, Secret reference inventory.
- Volumes, PVCs, mounts, emptyDir/tmpfs detection.
- Probe extraction and recent probe-failure correlation.
- HPA condition parsing and Deployment/HPA conflict detection.
- Dependency-aware checks such as Valkey / Redis-compatible config clues.
- Captures browser metadata and invalid heap classification.
- Background activity / refresh policy state.

## Migration Strategy

### Phase 1: Stop Growing Python

- Keep existing shell/Python code working.
- Avoid adding new Python parsing blocks for new UX/workflow features.
- Add any new structured inventory logic in Go.
- Keep shell commands as wrappers where useful.

### Phase 2: Introduce A Go Helper Surface

Add a Go command/helper that can emit the same plain-language output currently produced by JSON-heavy shell scripts.

Possible shapes:

- Extend `tui/jdebug-tui` with non-interactive subcommands.
- Add a small Go helper binary under `tui/` or `cmd/`.
- Have shell scripts call the Go helper when present, and fall back to Python temporarily.

Example commands:

```sh
jdebug-tui inspect topology --namespace debug-demo --pod pod-a
jdebug-tui inspect why --namespace debug-demo --pod pod-a
jdebug-tui inspect security --namespace debug-demo --pod pod-a
```

### Phase 3: Move High-Value Scripts First

Migrate the scripts with the most structured JSON parsing and highest UX value first:

1. `topology` / workload context.
2. `why` / pod deep-dive.
3. `security` / pod security posture.
4. `memory` JSON parsing and calculations.
5. lifecycle ownership checks.

### Phase 4: Make Python Optional

Once Go equivalents exist:

- Prefer Go implementation automatically.
- Keep Python fallbacks only for compatibility during transition.
- Update docs so Python is no longer required for normal diagnostics.
- Keep Python only for tests if still useful.

## Design Constraints

- Preserve the current CLI contract and output quality.
- Preserve plain-language explanations and next steps.
- Preserve low-dependency shell usage where the Go TUI is not built.
- Do not require a live cluster for unit tests; use fixtures for Kubernetes JSON.
- Do not print Secret values; show names, keys, and references only.
- Avoid adding `jq` as a replacement dependency.

## Testing Direction

For each migrated command, add Go tests around fixture JSON:

- Normal healthy state.
- Missing optional fields.
- RBAC/forbidden or missing data paths.
- HPA active/failing/maxed.
- Deployment/HPA replica conflicts.
- Old ReplicaSets still serving pods.
- Services/ports/endpoints mapping.
- Probes, volumes, env/envFrom, Secret/ConfigMap references.
- Redaction behavior.

Keep shell-level tests to confirm the public command still works and the output still carries the expected plain-language findings.

## Expected Outcome

The long-term goal is:

- `make tui` needs Go and make.
- Normal rich diagnostics use Go parsing, not host Python.
- Shell scripts remain thin, readable wrappers.
- Python becomes optional for runtime use, possibly retained only for tests.

This reduces setup friction on a new Mac and keeps the project moving toward one structured implementation language for complex operator intelligence.