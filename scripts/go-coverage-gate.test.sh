#!/usr/bin/env bash
# Behavioural harness for scripts/go-coverage-gate.sh.
#
# Every case drives the DEPLOYED script through its real entrypoint against a
# generated Go module. Nothing here re-implements the gate's logic, because a
# harness that reasons about the gate instead of running it proves nothing.
#
# The suite carries positive controls as well as negative ones: a gate that
# failed everything would score 100% on negative cases alone.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
GATE="$ROOT_DIR/scripts/go-coverage-gate.sh"
# Fixtures must stay below this checkout rather than an OS temporary directory.
# The guarded PID leaf is exclusive and cleanup cannot escape TMP_ROOT.
TMP_BASE="${COVERAGE_GATE_TEST_WORK_ROOT:-$ROOT_DIR/.go-coverage-gate-test-work}"
mkdir -p -- "$TMP_BASE"
TMP_ROOT="$TMP_BASE/run-$$"
if ! mkdir -- "$TMP_ROOT"; then
  printf 'go-coverage-gate.test: cannot allocate fixture directory: %s\n' "$TMP_ROOT" >&2
  exit 2
fi
trap 'rm -rf -- "$TMP_ROOT"' EXIT

PASS_COUNT=0
OUT="$TMP_ROOT/out.txt"

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; printf -- '--- gate output ---\n%s\n-------------------\n' "$(cat "$OUT" 2>/dev/null)" >&2; exit 1; }

run_gate() {
  local dir="$1"; shift
  set +e
  ( cd "$dir" && GOFLAGS= bash "$GATE" --dir "$dir" "$@" ) >"$OUT" 2>&1
  GATE_STATUS=$?
  set -e
}

# Preserve the established positional-module and COVERAGE_FLOOR interface while
# exercising the same real gate entrypoint.
run_gate_legacy_interface() {
  local dir="$1"
  set +e
  ( cd "$dir" && GOFLAGS= bash "$GATE" "$dir" ) >"$OUT" 2>&1
  GATE_STATUS=$?
  set -e
}

expect_status() {
  local label="$1" want="$2"
  [[ "$GATE_STATUS" -eq "$want" ]] || fail "$label: expected exit $want, got $GATE_STATUS"
}

expect_output() {
  local label="$1" needle="$2"
  grep -Fq -- "$needle" "$OUT" || fail "$label: output is missing the literal: $needle"
}

expect_no_output() {
  local label="$1" needle="$2"
  grep -Fq -- "$needle" "$OUT" && fail "$label: output unexpectedly contains: $needle"
  return 0
}

new_module() {
  local dir="$TMP_ROOT/$1"
  mkdir -p "$dir"
  printf 'module %s\n\ngo 1.22\n' "$1" >"$dir/go.mod"
  printf '%s' "$dir"
}

# A package whose test covers 9 of 10 statements (90%).
add_good_package() {
  local dir="$1" name="$2"
  mkdir -p "$dir/$name"
  {
    printf 'package %s\n\n' "$name"
    for i in $(seq 1 10); do printf 'func F%d() int { return %d }\n' "$i" "$i"; done
  } >"$dir/$name/code.go"
  {
    printf 'package %s\n\nimport "testing"\n\n' "$name"
    printf 'func TestCovered(t *testing.T) {\n\tsum := 0\n'
    for i in $(seq 1 9); do printf '\tsum += F%d()\n' "$i"; done
    printf '\tif sum == 0 {\n\t\tt.Fatal("no work")\n\t}\n}\n'
  } >"$dir/$name/code_test.go"
}

# A package whose test covers `covered` of `total` statements.
add_ratio_package() {
  local dir="$1" name="$2" total="$3" covered="$4"
  mkdir -p "$dir/$name"
  {
    printf 'package %s\n\n' "$name"
    for i in $(seq 1 "$total"); do printf 'func F%d() int { return %d }\n' "$i" "$i"; done
  } >"$dir/$name/code.go"
  {
    printf 'package %s\n\nimport "testing"\n\n' "$name"
    printf 'func TestCovered(t *testing.T) {\n\tsum := 0\n'
    for i in $(seq 1 "$covered"); do printf '\tsum += F%d()\n' "$i"; done
    printf '\tif sum == 0 {\n\t\tt.Fatal("no work")\n\t}\n}\n'
  } >"$dir/$name/code_test.go"
}

# ---------------------------------------------------------------------------
# POSITIVE CONTROL - a clean module passes. Without this, a gate that failed
# every input would score perfectly on the negative cases below.
# ---------------------------------------------------------------------------
DIR="$(new_module clean)"
add_good_package "$DIR" alpha
add_good_package "$DIR" beta
run_gate "$DIR" --floor 85
expect_status "clean module passes" 0
expect_output "clean module passes" "units_measured=2"
expect_output "clean module passes" "minimum_module_coverage=90.0%"
expect_output "clean module passes" "go-coverage-gate: PASS"
pass "clean module at 90% passes the 85% floor and reports units_measured=2"

# ---------------------------------------------------------------------------
# The reported number is the MINIMUM, never the average. Two packages at 90%
# and 50% average to 70%, which is above nothing that matters - the 50% package
# must fail on its own.
# ---------------------------------------------------------------------------
DIR="$(new_module belowfloor)"
add_good_package "$DIR" alpha
add_ratio_package "$DIR" bravo 10 5
run_gate "$DIR" --floor 85
expect_status "sub-floor package fails" 1
expect_output "sub-floor package fails" "50.0%   belowfloor/bravo"
expect_output "sub-floor package fails" "[5 of 10 statements, floor 85%]"
expect_output "sub-floor package fails" "minimum_module_coverage=50.0%"
expect_output "sub-floor package fails" "packages_below_floor=1"
expect_output "sub-floor package fails" "units_measured=2"
# The compliant sibling must still be reported. A gate that only prints failures
# cannot be audited for completeness.
expect_output "sub-floor package fails" "90.0%   belowfloor/alpha"
pass "a single sub-floor package fails the run while its 90% sibling still reports"

# ---------------------------------------------------------------------------
# ESCAPE HATCH (b) - MISSING ROW. A package with no test files must APPEAR at
# 0.0% and FAIL. This is the defect the previous awk gate carried: its rule body
# fired only on /coverage:/ lines, so `?  pkg  [no test files]` was invisible.
# ---------------------------------------------------------------------------
DIR="$(new_module notests)"
add_good_package "$DIR" alpha
mkdir -p "$DIR/orphan"
{
  printf 'package orphan\n\n'
  printf 'func Add(a, b int) int {\n\tif a > b {\n\t\treturn a + b\n\t}\n\treturn a - b\n}\n'
} >"$DIR/orphan/code.go"
run_gate "$DIR" --floor 85
expect_status "test-less package fails" 1
expect_output "test-less package fails" "0.0%   notests/orphan"
expect_output "test-less package fails" "[NO TEST FILES: 0 of"
expect_output "test-less package fails" "units_measured=2"
expect_output "test-less package fails" "minimum_module_coverage=0.0%"
pass "a package with no test files appears in the report at 0.0% and fails"

# The same package under the OLD awk rule body, to prove the case is real and
# that the old shape genuinely let it through.
printf '?   \tnotests/orphan\t[no test files]\n' >"$TMP_ROOT/legacy.txt"
set +e
awk '/coverage:/ { v = $5; sub(/%/, "", v); if ((v + 0) < 85) { failed = 1 } } END { exit failed }' \
  "$TMP_ROOT/legacy.txt" >/dev/null 2>&1
LEGACY_STATUS=$?
set -e
[[ "$LEGACY_STATUS" -eq 0 ]] || fail "legacy control: the retired awk rule was expected to pass this line"
pass "negative control: the retired awk rule body exits 0 on the very line the new gate fails"

# ---------------------------------------------------------------------------
# ESCAPE HATCH (a) - EMPTY REPORT. Measuring zero packages is a hard failure.
# ---------------------------------------------------------------------------
DIR="$(new_module emptymod)"
run_gate "$DIR" --floor 85
expect_status "empty package set fails" 1
expect_output "empty package set fails" "units_measured=0"
expect_no_output "empty package set fails" "go-coverage-gate: PASS"
pass "a module with zero packages hard-fails instead of exiting 0 over an empty set"

# ---------------------------------------------------------------------------
# The rounding trap. 288 of 339 statements is 84.9558%, which `go test -cover`
# PRINTS as "85.0%". A gate that parses the printed string passes it; a gate
# that compares integers fails it. This is the case that separates the two.
# ---------------------------------------------------------------------------
DIR="$(new_module roundmod)"
add_ratio_package "$DIR" edge 339 288
PRINTED="$( (cd "$DIR" && GOFLAGS= go test -count=1 -cover ./...) 2>&1 || true)"
case "$PRINTED" in
  *"85.0% of statements"*) : ;;
  *) fail "rounding trap: go test -cover did not print 85.0% for 288/339; it printed: $PRINTED" ;;
esac
run_gate "$DIR" --floor 85
expect_status "rounding trap fails" 1
expect_output "rounding trap fails" "roundmod/edge"
expect_output "rounding trap fails" "288 of 339 statements"
pass "288/339 prints as 85.0% but fails the integer comparison"

# ---------------------------------------------------------------------------
# Test integrity. A skipped test is not a passing test.
# ---------------------------------------------------------------------------
DIR="$(new_module skipmod)"
add_good_package "$DIR" alpha
cat >"$DIR/alpha/skip_test.go" <<'EOF'
package alpha

import "testing"

func TestSkipped(t *testing.T) {
	t.Skip("deliberately skipped")
}
EOF
run_gate "$DIR" --floor 85
expect_status "skipped test fails" 1
expect_output "skipped test fails" "SKIPPED TEST"
expect_output "skipped test fails" "TestSkipped"
pass "a skipped test fails the run even though coverage is above the floor"

# ---------------------------------------------------------------------------
DIR="$(new_module failmod)"
add_good_package "$DIR" alpha
cat >"$DIR/alpha/fail_test.go" <<'EOF'
package alpha

import "testing"

func TestBroken(t *testing.T) {
	t.Fatal("deliberate failure")
}
EOF
run_gate "$DIR" --floor 85
expect_status "failing test fails" 1
expect_output "failing test fails" "FAILING TEST"
expect_output "failing test fails" "TestBroken"
pass "a failing test fails the run and is named in the report"

# ---------------------------------------------------------------------------
DIR="$(new_module assertmod)"
add_good_package "$DIR" alpha
cat >"$DIR/alpha/hollow_test.go" <<'EOF'
package alpha

import "testing"

func TestHollow(t *testing.T) {
	_ = F1()
}
EOF
run_gate "$DIR" --floor 85
expect_status "assertion-free test fails" 1
expect_output "assertion-free test fails" "ASSERTION-FREE TEST"
expect_output "assertion-free test fails" "TestHollow"
pass "a test that never references its *testing.T parameter fails the run"

# OVER-BLOCK CONTROL for the same detector: a test that delegates its checking
# to a helper still passes t, and must NOT be reported.
DIR="$(new_module delegatemod)"
add_good_package "$DIR" alpha
cat >"$DIR/alpha/delegate_test.go" <<'EOF'
package alpha

import "testing"

func requireNonZero(t *testing.T, got int) {
	t.Helper()
	if got == 0 {
		t.Fatalf("got %d", got)
	}
}

func TestDelegates(t *testing.T) {
	requireNonZero(t, F1())
}
EOF
run_gate "$DIR" --floor 85
expect_status "delegating test passes" 0
expect_no_output "delegating test passes" "ASSERTION-FREE TEST"
pass "over-block control: a test that delegates assertions to a helper is not flagged"

# ---------------------------------------------------------------------------
# OVER-BLOCK CONTROL. A package with no coverable statements but with a test
# must be reported and must PASS. The retired awk gate false-failed exactly this
# shape: `coverage: [no statements]` parsed to the token "[no" and became 0%.
# ---------------------------------------------------------------------------
DIR="$(new_module nostmtmod)"
add_good_package "$DIR" alpha
mkdir -p "$DIR/consts"
cat >"$DIR/consts/consts.go" <<'EOF'
package consts

const Answer = 42

type Thing struct{ Name string }
EOF
cat >"$DIR/consts/consts_test.go" <<'EOF'
package consts

import "testing"

func TestAnswer(t *testing.T) {
	if Answer != 42 {
		t.Fatal("bad")
	}
}
EOF
run_gate "$DIR" --floor 85
expect_status "statement-less package passes" 0
expect_output "statement-less package passes" "nostmtmod/consts"
expect_output "statement-less package passes" "no coverable statements"
expect_output "statement-less package passes" "packages_with_no_coverable_statements=1"
expect_output "statement-less package passes" "units_measured=2"
pass "over-block control: a tested package with no coverable statements is reported and passes"

# The retired awk rule failed that same shape, naming the wrong thing.
printf 'ok  \tnostmtmod/consts\t0.170s\tcoverage: [no statements]\n' >"$TMP_ROOT/legacy2.txt"
set +e
awk '/coverage:/ { v = $5; sub(/%/, "", v); if ((v + 0) < 85) { failed = 1 } } END { exit failed }' \
  "$TMP_ROOT/legacy2.txt" >/dev/null 2>&1
LEGACY2_STATUS=$?
set -e
[[ "$LEGACY2_STATUS" -eq 1 ]] || fail "legacy control: the retired awk rule was expected to false-fail this line"
pass "negative control: the retired awk rule false-failed a fully tested statement-less package"

# ---------------------------------------------------------------------------
# Floor validation. A consumer may RAISE the fleet floor, never lower it, and a
# bad floor must be rejected BEFORE any measurement runs.
# ---------------------------------------------------------------------------
DIR="$(new_module floormod)"
add_ratio_package "$DIR" alpha 100 86

run_gate "$DIR" --floor 84
expect_status "floor below fleet floor is rejected" 2
expect_output "floor below fleet floor is rejected" "below the fleet floor"
expect_no_output "floor below fleet floor is rejected" "units_measured"
pass "a floor of 84 is rejected before any measurement runs"

run_gate "$DIR" --floor notanumber
expect_status "non-numeric floor is rejected" 2
expect_output "non-numeric floor is rejected" "must be a whole number"
pass "a non-numeric floor is rejected"

run_gate "$DIR" --floor 101
expect_status "floor above 100 is rejected" 2
expect_output "floor above 100 is rejected" "exceeds 100"
pass "a floor above 100 is rejected"

run_gate "$DIR" --floor 85
expect_status "default fleet floor passes" 0
expect_output "default fleet floor passes" "floor=85% (fleet floor 85%)"
pass "an 86.0% package passes at the 85% fleet floor"

run_gate "$DIR" --floor 90
expect_status "raised floor is enforced" 1
expect_output "raised floor is enforced" "floor=90% (fleet floor 85%)"
expect_output "raised floor is enforced" "floor 90%"
expect_output "raised floor is enforced" "packages_below_floor=1"
pass "the same 86.0% package fails once the consumer raises the floor to 90"

COVERAGE_FLOOR=90 run_gate_legacy_interface "$DIR"
expect_status "legacy positional interface and COVERAGE_FLOOR are enforced" 1
expect_output "legacy positional interface and COVERAGE_FLOOR are enforced" "floor=90% (fleet floor 85%)"
pass "the existing positional module interface and COVERAGE_FLOOR override remain enforced"

# Exactly at the floor passes. An off-by-one at the boundary would either fail
# compliant work or admit non-compliant work, and both are silent.
DIR="$(new_module boundarymod)"
add_ratio_package "$DIR" alpha 100 85
run_gate "$DIR" --floor 85
expect_status "exactly at the floor passes" 0
expect_output "exactly at the floor passes" "minimum_module_coverage=85.0%"
pass "a package at exactly 85.0% passes the 85% floor"

add_ratio_package "$DIR" bravo 100 84
run_gate "$DIR" --floor 85
expect_status "one below the floor fails" 1
expect_output "one below the floor fails" "84.0%   boundarymod/bravo"
expect_output "one below the floor fails" "packages_below_floor=1"
pass "a package one statement below the floor fails while its boundary sibling passes"

printf '\n%d checks passed\n' "$PASS_COUNT"
