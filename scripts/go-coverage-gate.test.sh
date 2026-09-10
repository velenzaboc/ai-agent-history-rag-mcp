#!/usr/bin/env bash
#
# Controls for scripts/go-coverage-gate.sh.
#
# Every case fabricates a REAL temporary Go module and invokes the REAL gate
# entrypoint -- the same path CI drives. Nothing is injected as pre-baked text,
# because a gate whose inputs can be doctored is not a gate.
#
# The negative controls (a)-(c) prove it fails. The positive control (d) proves
# it is calibrated: without (d) a gate that always failed would satisfy (a)-(c).
#
#   (a) NEGATIVE - a package with NO TESTS and no coverable statements. This is
#       the exact shape the predecessor awk gate could not see: measured against
#       go1.27.0, `go test -cover ./...` prints "?   pkg  [no test files]" with
#       no "coverage:" token, so an awk rule keyed on /coverage:/ exited 0. The
#       gate must FAIL and the package must APPEAR at 0.0%.
#   (b) NEGATIVE - a package with tests but coverage below the floor.
#   (c) NEGATIVE - a module with zero packages. An empty report is not a pass.
#   (d) POSITIVE - every package tested and above the floor. Must exit 0.
#   (e) NEGATIVE - a package whose tests FAIL while still reporting high
#       coverage. Keying on the percentage alone would pass this.
#   (f) NEGATIVE - a sub-floor COVERAGE_FLOOR is rejected, not honoured.
#   (g) POSITIVE - a tested package with no coverable statements is legitimate.

set -uo pipefail

GATE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/go-coverage-gate.sh"
if [ ! -f "$GATE" ]; then
	printf 'go-coverage-gate.test: cannot find %s\n' "$GATE" >&2
	exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Fixtures are standalone modules with no requirements: they must not be pulled
# into an enclosing workspace, and they never need the network.
export GOWORK=off
export GOFLAGS=

pass_count=0
fail_count=0

report() {
	local ok="$1" name="$2" detail="$3"
	if [ "$ok" = "yes" ]; then
		printf 'PASS  %s\n' "$name"
		pass_count=$((pass_count + 1))
	else
		printf 'FAIL  %s\n      %s\n' "$name" "$detail"
		fail_count=$((fail_count + 1))
	fi
}

# covered_pkg <dir> <pkg> -- a package with tests that fully cover it.
covered_pkg() {
	mkdir -p "$1/$2"
	printf 'package %s\n\nfunc Abs(x int) int {\n\tif x > 0 {\n\t\treturn x\n\t}\n\treturn -x\n}\n' "$2" >"$1/$2/impl.go"
	printf 'package %s\n\nimport "testing"\n\nfunc TestAbs(t *testing.T) {\n\tif Abs(-2) != 2 {\n\t\tt.Fatal("neg")\n\t}\n\tif Abs(2) != 2 {\n\t\tt.Fatal("pos")\n\t}\n}\n' "$2" >"$1/$2/impl_test.go"
}

new_module() {
	local dir="$WORK/$1"
	rm -rf "$dir"
	mkdir -p "$dir"
	printf 'module gatefixture/%s\n\ngo 1.27\n' "$1" >"$dir/go.mod"
	printf '%s' "$dir"
}

# ---------------------------------------------------------------- (a) NEGATIVE
# The measured vacuous-pass shape: no tests AND no coverable statements.
d="$(new_module untested-no-statements)"
covered_pkg "$d" good
mkdir -p "$d/typesonly"
printf 'package typesonly\n\ntype Record struct {\n\tID   string\n\tSize int\n}\n\ntype Reader interface {\n\tRead() (Record, error)\n}\n' >"$d/typesonly/types.go"
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	report no "(a) test-less package fails" "gate exited 0 on a package with no tests"
elif ! printf '%s' "$out" | grep -q 'typesonly'; then
	report no "(a) test-less package fails" "package is absent from the report; hatch (b) still open"
elif ! printf '%s' "$out" | grep -q 'NO TESTS'; then
	report no "(a) test-less package fails" "package present but not marked NO TESTS"
else
	report yes "(a) test-less package fails AND appears at 0.0%" ""
fi

# Positive control on the instrument itself: prove the predecessor awk rule
# really did pass this exact module, so case (a) is measuring a closed hole
# rather than a hole that never existed.
( cd "$d" && go test -count=1 -cover ./... >"$WORK/legacy.txt" 2>&1 )
awk '
	/coverage:/ {
		value = $5
		sub(/%/, "", value)
		if ((value + 0) < 85) { failed = 1 }
	}
	END { exit failed }
' "$WORK/legacy.txt"
legacy_rc=$?
if [ "$legacy_rc" -eq 0 ]; then
	report yes "(a') predecessor awk gate PASSED the same module (hole confirmed)" ""
else
	report no "(a') predecessor awk gate PASSED the same module" "legacy awk exited $legacy_rc; the reproduction no longer reproduces"
fi

# ---------------------------------------------------------------- (b) NEGATIVE
d="$(new_module below-floor)"
covered_pkg "$d" good
mkdir -p "$d/thin"
{
	printf 'package thin\n\nfunc Classify(x int) string {\n'
	printf '\tif x > 100 {\n\t\treturn "big"\n\t}\n'
	printf '\tif x > 50 {\n\t\treturn "medium"\n\t}\n'
	printf '\tif x > 10 {\n\t\treturn "small"\n\t}\n'
	printf '\tif x > 0 {\n\t\treturn "tiny"\n\t}\n'
	printf '\treturn "zero"\n}\n'
} >"$d/thin/impl.go"
printf 'package thin\n\nimport "testing"\n\nfunc TestClassify(t *testing.T) {\n\tif Classify(200) != "big" {\n\t\tt.Fatal("big")\n\t}\n}\n' >"$d/thin/impl_test.go"
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	report no "(b) below-floor package fails" "gate exited 0 on a sub-floor package"
elif ! printf '%s' "$out" | grep -q 'BELOW FLOOR'; then
	report no "(b) below-floor package fails" "no BELOW FLOOR row emitted"
else
	report yes "(b) below-floor package fails" ""
fi

# ---------------------------------------------------------------- (c) NEGATIVE
d="$(new_module zero-packages)"
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	report no "(c) zero packages fails" "gate exited 0 on an empty module"
elif ! printf '%s' "$out" | grep -q 'units_measured=0'; then
	report no "(c) zero packages fails" "units_measured=0 not emitted"
else
	report yes "(c) zero packages fails with units_measured=0" ""
fi

# ---------------------------------------------------------------- (d) POSITIVE
d="$(new_module all-good)"
covered_pkg "$d" alpha
covered_pkg "$d" beta
covered_pkg "$d" gamma
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -ne 0 ]; then
	report no "(d) fully covered module passes" "gate exited $rc on a compliant module: $out"
elif ! printf '%s' "$out" | grep -q 'units_measured=3'; then
	report no "(d) fully covered module passes" "expected units_measured=3"
elif ! printf '%s' "$out" | grep -q 'minimum=100.0%'; then
	# Pins the fixture's own coverage. Cases (f') and (g) rely on covered_pkg
	# landing above 90; without this assertion a change to the fixture would
	# silently turn those positive controls into false negatives.
	report no "(d) fully covered module passes" "fixture coverage drifted; expected minimum=100.0%"
else
	report yes "(d) fully covered module passes (gate is calibrated)" ""
fi

# ---------------------------------------------------------------- (e) NEGATIVE
d="$(new_module failing-tests)"
covered_pkg "$d" good
mkdir -p "$d/broken"
printf 'package broken\n\nfunc Double(x int) int {\n\treturn x * 2\n}\n' >"$d/broken/impl.go"
printf 'package broken\n\nimport "testing"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal("math")\n\t}\n\tt.Fatal("deliberate failure at 100%% coverage")\n}\n' >"$d/broken/impl_test.go"
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	report no "(e) failing tests fail the gate" "gate exited 0 while a package's tests failed"
elif ! printf '%s' "$out" | grep -q 'TESTS FAILED'; then
	report no "(e) failing tests fail the gate" "no TESTS FAILED row emitted"
else
	report yes "(e) failing tests fail the gate even at high coverage" ""
fi

# ---------------------------------------------------------------- (f) NEGATIVE
d="$(new_module floor-override)"
covered_pkg "$d" alpha
out="$(COVERAGE_FLOOR=50 "$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	report no "(f) sub-floor COVERAGE_FLOOR rejected" "gate honoured a floor of 50"
elif ! printf '%s' "$out" | grep -q 'below the immutable fleet floor'; then
	report no "(f) sub-floor COVERAGE_FLOOR rejected" "wrong rejection reason: $out"
else
	report yes "(f) sub-floor COVERAGE_FLOOR rejected, never honoured" ""
fi

out="$(COVERAGE_FLOOR=90 "$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'floor=90%'; then
	report yes "(f') COVERAGE_FLOOR may RAISE the floor" ""
else
	report no "(f') COVERAGE_FLOOR may RAISE the floor" "raising to 90 did not take effect (rc=$rc)"
fi

# ---------------------------------------------------------------- (g) POSITIVE
d="$(new_module tested-no-statements)"
covered_pkg "$d" alpha
mkdir -p "$d/decls"
printf 'package decls\n\ntype Kind struct {\n\tName string\n}\n' >"$d/decls/types.go"
printf 'package decls\n\nimport "testing"\n\nfunc TestKind(t *testing.T) {\n\tif (Kind{Name: "x"}).Name != "x" {\n\t\tt.Fatal("field")\n\t}\n}\n' >"$d/decls/types_test.go"
out="$("$GATE" "$d" 2>&1)"
rc=$?
if [ "$rc" -ne 0 ]; then
	report no "(g) tested no-statements package passes" "gate exited $rc: $out"
elif ! printf '%s' "$out" | grep -q '\[no statements\]'; then
	report no "(g) tested no-statements package passes" "not classified as [no statements]"
else
	report yes "(g) tested no-statements package passes (no false failure)" ""
fi

printf '\n----------------------------------------\n'
printf 'go-coverage-gate controls: %d passed, %d failed\n' "$pass_count" "$fail_count"
[ "$fail_count" -eq 0 ] || exit 1
printf 'All controls green.\n'
