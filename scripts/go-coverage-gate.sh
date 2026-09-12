#!/usr/bin/env bash
# Per-module Go coverage and test-integrity gate.
#
# THE UNIT OF COMPLIANCE IS THE PACKAGE, NOT THE REPOSITORY.
# There is no mean, no total, no aggregate anywhere in this gate. Every package
# reported by `go list ./...` gets its own row and its own verdict, and the
# summary number is the MINIMUM across packages, never the average.
#
# Two escape hatches are closed by construction:
#   (a) EMPTY REPORT  - measuring zero packages is a hard failure, never a quiet
#                       exit 0 over an empty set.
#   (b) MISSING ROW   - `go list ./...` is the denominator, so a package with no
#                       test files is forced to appear at 0.0% and FAIL. It can
#                       never be absent from the report.
#
# Also enforced in the same run: every test passes, and no test is skipped or
# assertion-free. A skipped test is not a passing test.
#
# Usage: scripts/go-coverage-gate.sh [--floor N] [--dir PATH]
set -euo pipefail

FLEET_FLOOR=85
FLOOR="${COVERAGE_FLOOR:-$FLEET_FLOOR}"
MODULE_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --floor)
      [[ $# -ge 2 ]] || { printf 'go-coverage-gate: --floor requires a value\n' >&2; exit 2; }
      FLOOR="$2"; shift 2 ;;
    --dir)
      [[ $# -ge 2 ]] || { printf 'go-coverage-gate: --dir requires a value\n' >&2; exit 2; }
      MODULE_DIR="$2"; shift 2 ;;
    -h|--help)
      printf 'usage: go-coverage-gate.sh [--floor N] [--dir PATH]\n'; exit 0 ;;
    *)
      if [[ -z "$MODULE_DIR" ]]; then
        MODULE_DIR="$1"; shift
      else
        printf 'go-coverage-gate: unknown argument: %s\n' "$1" >&2; exit 2
      fi ;;
  esac
done

# A consumer may RAISE the fleet floor, never lower it. Validate before any
# measurement runs, so a bad floor can never reach a green run.
if [[ ! "$FLOOR" =~ ^[0-9]+$ ]]; then
  printf 'go-coverage-gate: floor must be a whole number, got %s\n' "$FLOOR" >&2
  exit 2
fi
if (( FLOOR < FLEET_FLOOR )); then
  printf 'go-coverage-gate: floor %s is below the fleet floor of %s; a consumer may raise the floor, never lower it\n' \
    "$FLOOR" "$FLEET_FLOOR" >&2
  exit 2
fi
if (( FLOOR > 100 )); then
  printf 'go-coverage-gate: floor %s exceeds 100\n' "$FLOOR" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ -n "$MODULE_DIR" ]]; then
  cd -P -- "$MODULE_DIR"
fi
MODULE_ROOT="$(pwd -P)"

# A caller's GOFLAGS can focus, skip, list, benchmark, or fuzz only a subset of
# tests. The gate measures the whole module, so it deliberately neutralizes all
# caller Go flags before both package discovery and test execution.
export GOFLAGS=

for tool in go awk jq sort; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf 'go-coverage-gate: required tool not found: %s\n' "$tool" >&2
    exit 2
  }
done

# Keep transient gate state inside the physical measured checkout. An override
# is intentionally rejected: an arbitrary path or symlink would make cleanup
# capable of escaping the checkout.
if [[ -v COVERAGE_GATE_WORK_ROOT ]]; then
  printf 'go-coverage-gate: COVERAGE_GATE_WORK_ROOT is unsupported; scratch stays in the physical checkout\n' >&2
  exit 2
fi
WORK_ROOT="$MODULE_ROOT/.coverage-gate-work"
if [[ -L "$WORK_ROOT" ]]; then
  printf 'go-coverage-gate: scratch root must not be a symlink: %s\n' "$WORK_ROOT" >&2
  exit 2
fi
if [[ -e "$WORK_ROOT" && ! -d "$WORK_ROOT" ]]; then
  printf 'go-coverage-gate: scratch root is not a directory: %s\n' "$WORK_ROOT" >&2
  exit 2
fi
if [[ ! -e "$WORK_ROOT" ]] && ! mkdir -- "$WORK_ROOT"; then
  printf 'go-coverage-gate: cannot create checkout-local scratch root: %s\n' "$WORK_ROOT" >&2
  exit 2
fi
if [[ -L "$WORK_ROOT" ]]; then
  printf 'go-coverage-gate: scratch root must not be a symlink: %s\n' "$WORK_ROOT" >&2
  exit 2
fi
WORK="$WORK_ROOT/run-$$"
if ! mkdir -- "$WORK"; then
  printf 'go-coverage-gate: cannot allocate isolated work directory: %s\n' "$WORK" >&2
  exit 2
fi
trap 'rm -rf -- "$WORK"' EXIT

hard_fail() {
  printf 'go-coverage-gate: HARD FAIL: %s\n' "$1" >&2
  exit 1
}

# ---------------------------------------------------------------------------
# 1. Denominator. `go list ./...` is the authoritative module set. It must
#    succeed, and it must be non-empty, or the gate fails closed. A gate that
#    silently measures a short list is the same vacuum with better-looking code.
# ---------------------------------------------------------------------------
if ! go list -f '{{.ImportPath}}	{{len .TestGoFiles}}	{{len .XTestGoFiles}}' ./... \
     >"$WORK/packages.raw" 2>"$WORK/golist.err"; then
  printf '%s\n' "$(cat "$WORK/golist.err")" >&2
  hard_fail "go list ./... exited non-zero; the package denominator is unknown"
fi
if [[ -s "$WORK/golist.err" ]]; then
  printf 'go-coverage-gate: go list stderr:\n%s\n' "$(cat "$WORK/golist.err")" >&2
  if grep -Eq 'build constraints exclude|no Go files|cannot find|malformed' "$WORK/golist.err"; then
    hard_fail "go list ./... reported an unresolved package; the denominator is incomplete"
  fi
fi

sort "$WORK/packages.raw" >"$WORK/packages.tsv"
UNITS_MEASURED="$(wc -l <"$WORK/packages.tsv" | tr -d ' ')"
if [[ "$UNITS_MEASURED" -eq 0 ]]; then
  hard_fail "units_measured=0 - the gate measured no packages; an empty set is never a pass"
fi

# ---------------------------------------------------------------------------
# 2. Run the suite once: coverage profile for the numbers, -json for integrity.
# ---------------------------------------------------------------------------
PROFILE="$WORK/coverage.out"
EVENTS="$WORK/events.json"
set +e
go test -count=1 -covermode=atomic -coverprofile="$PROFILE" -json ./... \
  >"$EVENTS" 2>"$WORK/gotest.err"
GO_TEST_STATUS=$?
set -e

if [[ -s "$WORK/gotest.err" ]]; then
  printf 'go-coverage-gate: go test stderr:\n%s\n' "$(cat "$WORK/gotest.err")" >&2
fi

# ---------------------------------------------------------------------------
# 3. Test integrity. 100% of tests pass; none skipped; none assertion-free.
#    A package-level "fail" event is how a build failure arrives, so it is
#    caught here too rather than being filtered out with the test-level events.
# ---------------------------------------------------------------------------
INTEGRITY_FAILED=0

jq -r 'select(.Action=="fail") | "FAILING TEST  " + .Package + (if .Test then " :: " + .Test else " :: (package-level failure)" end)' \
  "$EVENTS" >"$WORK/failures.txt" || hard_fail "could not parse go test -json output"
jq -r 'select(.Action=="skip" and (.Test != null)) | "SKIPPED TEST  " + .Package + " :: " + .Test' \
  "$EVENTS" >"$WORK/skips.txt" || hard_fail "could not parse go test -json output"
TESTS_RUN="$(jq -r 'select(.Action=="pass" and (.Test != null)) | .Package + "::" + .Test' "$EVENTS" | sort -u | wc -l | tr -d ' ')"
jq -r 'select(.Action=="run" and (.Test != null)) | .Package' "$EVENTS" | sort -u >"$WORK/tests-executed.tsv" || hard_fail "could not parse go test -json output"

if [[ -s "$WORK/failures.txt" ]]; then
  printf '\n'; cat "$WORK/failures.txt" >&2; INTEGRITY_FAILED=1
fi
if [[ -s "$WORK/skips.txt" ]]; then
  printf '\n'; cat "$WORK/skips.txt" >&2; INTEGRITY_FAILED=1
fi
if [[ $GO_TEST_STATUS -ne 0 && ! -s "$WORK/failures.txt" ]]; then
  hard_fail "go test exited $GO_TEST_STATUS with no failure event; the run did not complete"
fi

# A Go parser owns source semantics here. Comments, quoted strings, raw strings,
# braces, and legal whitespace are syntax, not a raw-text approximation.
if ! go list -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' ./... >"$WORK/testfiles.txt" 2>"$WORK/testfiles.err"; then
  printf '%s\n' "$(cat "$WORK/testfiles.err")" >&2
  hard_fail "test-file enumeration failed; assertion integrity is unknown"
fi
if [[ ! -s "$WORK/testfiles.txt" ]]; then
  hard_fail "test-file enumeration returned no files; assertion integrity is unknown"
fi
set +e
go run "$SCRIPT_DIR/go-test-integrity/main.go" --files "$WORK/testfiles.txt" >"$WORK/assertionfree.txt" 2>"$WORK/assertionfree.err"
PARSER_STATUS=$?
set -e
if [[ $PARSER_STATUS -ne 0 ]]; then
  printf '\n'; cat "$WORK/assertionfree.txt" >&2; cat "$WORK/assertionfree.err" >&2
  INTEGRITY_FAILED=1
fi

# ---------------------------------------------------------------------------
# 4. Per-package statement counts, taken from the profile as INTEGERS.
#    `go test -cover` prints one decimal, so 84.96% renders as "85.0%" and a
#    string-parsing gate lets it through. Counting statements cannot round.
# ---------------------------------------------------------------------------
[[ -s "$PROFILE" ]] || hard_fail "no coverage profile was produced"
awk '
  NR == 1 && /^mode:/ { next }
  NF >= 3 {
    key = $1
    if (!(key in stmts)) { stmts[key] = $2 + 0; hit[key] = 0 }
    if (($3 + 0) > 0) hit[key] = 1
  }
  END {
    for (k in stmts) {
      file = k
      sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file)
      pkg = file
      sub(/\/[^\/]*$/, "", pkg)
      total[pkg] += stmts[k]
      if (hit[k]) covered[pkg] += stmts[k]
      seen[pkg] = 1
    }
    for (p in seen) printf "%s %d %d\n", p, covered[p] + 0, total[p]
  }
' "$PROFILE" >"$WORK/pkgcov.txt"
if [[ ! -s "$WORK/pkgcov.txt" ]]; then
  hard_fail "the coverage profile contains no statement blocks; the numbers cannot be trusted"
fi

# ---------------------------------------------------------------------------
# 5. Join coverage onto the denominator and rule on EVERY package.
# ---------------------------------------------------------------------------
set +e
awk -v floor="$FLOOR" -v expected="$UNITS_MEASURED" -v covfile="$WORK/pkgcov.txt" -v executedfile="$WORK/tests-executed.tsv" '
  FILENAME == covfile { cov[$1] = $2; tot[$1] = $3; next }
  FILENAME == executedfile { executed[$1] = 1; next }
  {
    pkg = $1; ntest = $2 + 0; nxtest = $3 + 0
    rows++
    t = (pkg in tot) ? tot[pkg] : 0
    c = (pkg in cov) ? cov[pkg] : 0
    hastests = (ntest + nxtest) > 0

    if (!hastests) {
      # Closes escape hatch (b): presence is mandatory. A package with no test
      # files appears at 0.0% and fails; it is never silently absent.
      printf "FAIL    %6.1f%%   %s   [NO TEST FILES: 0 of %d statements covered]\n", 0.0, pkg, t
      failed++
      if (!havemin || 0.0 < min) { min = 0.0; havemin = 1; minpkg = pkg }
      next
    }
    if (!(pkg in executed)) {
      printf "FAIL       0.0%%   %s   [NO TEST EXECUTED]\n", pkg
      failed++
      if (!havemin || 0.0 < min) { min = 0.0; havemin = 1; minpkg = pkg }
      next
    }
    if (t == 0) {
      printf "OK         n/a   %s   [no coverable statements]\n", pkg
      nostmt++
      next
    }
    pct = 100.0 * c / t
    if (c * 100 >= floor * t) {
      printf "OK      %6.1f%%   %s   [%d of %d statements]\n", pct, pkg, c, t
    } else {
      printf "FAIL    %6.1f%%   %s   [%d of %d statements, floor %d%%]\n", pct, pkg, c, t, floor
      failed++
    }
    if (!havemin || pct < min) { min = pct; havemin = 1; minpkg = pkg }
  }
  END {
    printf "\n"
    printf "units_measured=%d\n", rows + 0
    if (rows + 0 != expected + 0) {
      printf "HARD FAIL: emitted %d rows for %d packages; the report is incomplete\n", rows + 0, expected + 0
      exit 3
    }
    if (rows + 0 == 0) {
      printf "HARD FAIL: units_measured=0; an empty set is never a pass\n"
      exit 3
    }
    if (havemin) {
      printf "minimum_module_coverage=%.1f%% (%s)\n", min, minpkg
    } else {
      printf "minimum_module_coverage=n/a (no package has coverable statements)\n"
    }
    printf "packages_with_no_coverable_statements=%d\n", nostmt + 0
    printf "packages_below_floor=%d\n", failed + 0
    exit (failed + 0 > 0) ? 1 : 0
  }
' "$WORK/pkgcov.txt" "$WORK/tests-executed.tsv" "$WORK/packages.tsv"
COVERAGE_STATUS=$?
set -e

printf 'floor=%d%% (fleet floor %d%%)\n' "$FLOOR" "$FLEET_FLOOR"
printf 'tests_run=%s tests_skipped=%s tests_failed=%s\n' \
  "$TESTS_RUN" \
  "$(wc -l <"$WORK/skips.txt" | tr -d ' ')" \
  "$(wc -l <"$WORK/failures.txt" | tr -d ' ')"

if [[ $COVERAGE_STATUS -eq 3 ]]; then
  hard_fail "the per-package report did not cover every package"
fi
if [[ $COVERAGE_STATUS -ne 0 || $INTEGRITY_FAILED -ne 0 ]]; then
  printf '\ngo-coverage-gate: FAIL\n' >&2
  exit 1
fi

printf '\ngo-coverage-gate: PASS - every one of %s packages is at or above %d%%\n' "$UNITS_MEASURED" "$FLOOR"
