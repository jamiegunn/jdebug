#!/usr/bin/env bash
#
# freeze-spec.sh — snapshot the CLI's observable surface into spec/ golden
# files. This is rearchitecture Phase 0: "parity" during the Go migration is
# a diff against these files, not an opinion. Regenerate deliberately (and
# review the diff) when the surface changes on purpose:
#
#   scripts/freeze-spec.sh && git diff spec/
#
# Files:
#   spec/help.golden          jdebug --help (the command surface + one-liners)
#   spec/commands.golden      every dispatchable verb, extracted from the case arms
#   spec/assertions.golden    every behavioural assertion in the test suite —
#                             the executable spec, flattened for review

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
mkdir -p spec

./jdebug --help > spec/help.golden

# verbs: the dispatch case arms in jdebug (strip patterns, one per line)
awk '/^case "\$cmd" in/,/^esac/' jdebug \
    | grep -oE '^\s+[a-z|-]+\)' | tr -d ' )' | tr '|' '\n' | grep -v '^$' | LC_ALL=C sort -u \
    > spec/commands.golden

# the behavioural contract: every assertion the suite makes, with its label
grep -oE 'assert_(has|not|rc) +"[^"]+" +.*' tests/run-tests.sh \
    | sed 's/[[:space:]]*$//' > spec/assertions.golden

wc -l spec/*.golden | sed 's/^/  /'
echo "spec frozen — migration parity = 'this diff is empty (or deliberate)'"
