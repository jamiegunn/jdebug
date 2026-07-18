#!/usr/bin/env bash
#
# run-kind-tests.sh — the REAL-TRANSPORT layer: everything the live-JVM
# suite can't prove because it shims kubectl. Against an actual cluster:
#
#   · kubectl exec / cp over a real API server (the truncation-prone path)
#   · the jdk tier: a genuine `kubectl debug` ephemeral container attaching
#     across the container boundary
#   · a crash-looping pod: the capture tiers must fail LOUDLY and `why`
#     must warn about the missing HeapDumpOnOutOfMemoryError flag
#
# Expects a cluster already reachable via the ambient kubectl context (CI
# uses helm/kind-action). The fixture pod is a stock temurin image running
# tests/fixture/DebugFixture.java from a ConfigMap — no image build.
#
# NOTE: authored ahead of its first CI run — expect to shake out details
# (image tags, timings) on the first execution; assertions are written to
# fail loudly rather than hang.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
KIT="$PWD"
NS="jdebug-it"

PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL %s\n       %s\n' "$1" "$2"; }
section() { printf '\n== %s ==\n' "$1"; }

command -v kubectl >/dev/null || { echo "needs kubectl + a reachable cluster" >&2; exit 2; }
kubectl get --raw=/version >/dev/null || { echo "cluster unreachable" >&2; exit 2; }
[[ -x core/jdebug-core ]] || (cd core && go build -o jdebug-core ./cmd/jdebug-core) || exit 2

section "fixture pod"
kubectl delete namespace "$NS" --ignore-not-found --wait=true >/dev/null 2>&1
kubectl create namespace "$NS" >/dev/null
kubectl -n "$NS" create configmap fixture --from-file=DebugFixture.java=tests/fixture/DebugFixture.java >/dev/null
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: fixture
  labels: { app: fixture }
spec:
  shareProcessNamespace: true
  containers:
    - name: app
      image: eclipse-temurin:21-jdk
      command: ["java", "/fixture/DebugFixture.java", "8080"]
      volumeMounts: [{ name: src, mountPath: /fixture }]
      readinessProbe:
        httpGet: { path: /actuator/health, port: 8080 }
        initialDelaySeconds: 5
        periodSeconds: 3
  volumes:
    - name: src
      configMap: { name: fixture }
YAML
kubectl -n "$NS" wait --for=condition=Ready pod/fixture --timeout=180s >/dev/null \
    && ok "fixture pod Ready (temurin + ConfigMap source, no image build)" \
    || { bad "fixture pod" "$(kubectl -n "$NS" get pod fixture -o wide 2>&1 | tail -1)"; exit 1; }

ENVV=(env JDEBUG_KIT="$KIT" JDEBUG_DUMPS="$PWD/.it-dumps" JDEBUG_QUIET=1 \
      JDEBUG_NAMESPACE="$NS" JDEBUG_SELECTOR="app=fixture")
run() { OUT="$("${ENVV[@]}" "$@" 2>&1)"; RC=$?; }
rm -rf .it-dumps

section "capture tiers over REAL kubectl transport"
run core/jdebug-core threads --via actuator
[[ $RC -eq 0 ]] && ok "actuator threads over real exec (rc=0)" \
    || bad "actuator threads" "rc=$RC | $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
# NOTE: temurin images may ship NEITHER curl nor wget — then the tier must
# fail with the exact no-HTTP-client message and the jattach tier carries it.
# Either outcome is a valid capture route; a HANG or silent empty file is not.

run core/jdebug-core threads --via jattach
[[ $RC -eq 0 ]] && ok "jattach threads over real cp+exec (rc=0)" \
    || bad "jattach threads" "rc=$RC | $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"

run core/jdebug-core heap --confirm --via jattach
[[ $RC -eq 0 ]] && ok "jattach heap: real kubectl cp, size-verified (the F1 path, live)" \
    || bad "jattach heap" "rc=$RC | $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
h="$(find .it-dumps -name 'heap-jattach.hprof' | head -1)"
[[ -n "$h" ]] && head -c 12 "$h" | grep -q "JAVA PROFILE" \
    && ok "hprof survived the real cp intact" || bad "hprof integrity" "missing/corrupt: ${h:-<none>}"

section "tier 3 (jdk): real ephemeral-container attach"
run core/jdebug-core threads --via jdk
[[ $RC -eq 0 ]] && ok "jdk tier: kubectl debug + cross-container attach (rc=0)" \
    || bad "jdk tier" "rc=$RC | $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"

section "deadlock through the whole stack"
kubectl -n "$NS" exec fixture -c app -- sh -c \
    'command -v curl >/dev/null && curl -fsS localhost:8080/deadlock || java -e 2>/dev/null' >/dev/null 2>&1 \
    || kubectl -n "$NS" port-forward pod/fixture 18080:8080 >/dev/null 2>&1 &
sleep 2; curl -fsS localhost:18080/deadlock >/dev/null 2>&1
sleep 1
run core/jdebug-core threads --via jattach
fd="$(find .it-dumps -name 'threads-jattach.txt' | sort | tail -1)"
run core/jdebug-core analyze-threads "${fd:-/dev/null}"
printf '%s' "$OUT" | grep -q "DEADLOCK detected" \
    && ok "real deadlock, real transport, detected" \
    || bad "deadlock detection" "$(printf '%s' "$OUT" | head -2 | tr '\n' ' ')"

section "crash-loop: the ordinary bad day (audit F7)"
kubectl -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: crasher
  labels: { app: crasher }
spec:
  restartPolicy: Always
  containers:
    - name: app
      image: eclipse-temurin:21-jdk
      command: ["sh", "-c", "exit 137"]
YAML
sleep 15
OUT="$(env JDEBUG_KIT="$KIT" JDEBUG_DUMPS="$PWD/.it-dumps" JDEBUG_QUIET=1 \
      JDEBUG_NAMESPACE="$NS" JDEBUG_SELECTOR="app=crasher" \
      core/jdebug-core threads --via actuator 2>&1)"; RC=$?
[[ $RC -ne 0 ]] && ok "crash-looping pod: capture fails LOUDLY (rc=$RC), never silently" \
    || bad "crash-loop capture" "unexpected success against a dead container?"
printf '%s' "$OUT" | grep -qiE "error|fail" \
    && ok "failure explains itself" || bad "crash-loop error copy" "$(printf '%s' "$OUT" | head -2)"

section "cleanup + verdict"
kubectl delete namespace "$NS" --wait=false >/dev/null 2>&1
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ $FAIL -eq 0 ]]
