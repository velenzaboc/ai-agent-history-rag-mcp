#!/usr/bin/env bash
#
# Per-package Go coverage gate.
#
# THE UNIT OF COMPLIANCE IS THE PACKAGE, NOT THE REPOSITORY.
# There is no mean, no total, no aggregate and no "overall" number anywhere in
# this gate. It computes coverage PER PACKAGE and fails if ANY single package is
# below the floor. The reported figure is the MINIMUM across packages.
#
# Two escape hatches are closed by construction:
#
#   (a) EMPTY REPORT -> FAIL. `go list ./...` returning nothing is not a pass.
#       units_measured is emitted and zero is a hard failure.
#
#   (b) MISSING ROW -> a package with NO TESTS appears at 0.0% and FAILS. It is
#       never silently absent. `go list ./...` is the authoritative package set
#       and coverage is JOINED onto it, so a package cannot vanish by emitting
#       no coverage line.
#
# Hatch (b) is not hypothetical here. Measured against go1.27.0 on 2026-09-10,
# `go test -count=1 -cover ./...` reports an untested package that has coverable
# statements as "<TAB>pkg<TAB><TAB>coverage: 0.0% of statements", but reports an
# untested package with NO coverable statements (types only, consts only,
# interfaces only) as "?   <TAB>pkg<TAB>[no test files]" -- with no "coverage:"
# token at all, even under -cover. The predecessor gate here was an awk rule
# whose body fired only on /coverage:/ lines, so those packages were invisible
# to it and it exited 0. See scripts/go-coverage-gate.test.sh case (a), which
# reproduces that exact shape as a negative control.
#
# Test failures are caught on the STATUS TOKEN, not on the percentage. A package
# whose tests fail still emits a coverage percentage from the test binary, so a
# gate that only compares percentages passes a failing suite.
#
# Usage:  bash scripts/go-coverage-gate.sh [module-dir]
#
# The floor is immutable at 85. COVERAGE_FLOOR may RAISE it; any value below the
# fleet floor is rejected rather than honoured.

set -euo pipefail

readonly FLEET_FLOOR=85

floor="${COVERAGE_FLOOR:-$FLEET_FLOOR}"
case "$floor" in
	'' | *[!0-9]*)
		printf 'go-coverage-gate: COVERAGE_FLOOR must be a non-negative integer, got %s\n' "$floor" >&2
		exit 2
		;;
esac
if [ "$floor" -lt "$FLEET_FLOOR" ]; then
	printf 'go-coverage-gate: COVERAGE_FLOOR=%s is below the immutable fleet floor of %s%%.\n' "$floor" "$FLEET_FLOOR" >&2
	printf 'go-coverage-gate: a consumer may RAISE the floor, never lower it.\n' >&2
	exit 2
fi

if [ "$#" -gt 1 ]; then
	printf 'usage: %s [module-dir]\n' "$0" >&2
	exit 2
fi
module_dir="${1:-.}"
cd "$module_dir"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
packages="$workdir/packages.txt"
coverage="$workdir/coverage.txt"

# The AUTHORITATIVE package set. Coverage is joined onto this, never the reverse,
# so a package that emits no coverage line still gets a row.
if ! go list ./... >"$packages" 2>"$workdir/list.err"; then
	printf 'go-coverage-gate: `go list ./...` failed; refusing an unmeasured pass\n' >&2
	cat "$workdir/list.err" >&2
	exit 1
fi

# Escape hatch (a), closed in shell rather than in awk on purpose. `go list`
# exits 0 with only a stderr warning when "./..." matches nothing, and an awk
# two-file join cannot see an empty first file: with no record ever read from
# it, FNR==NR is still true for the FIRST record of the SECOND file, so the
# coverage output would be silently mistaken for the package set.
package_count="$(wc -l <"$packages" | tr -d '[:space:]')"
if [ "$package_count" -eq 0 ]; then
	printf 'units_measured=0\n'
	printf '\ngo-coverage-gate: FAIL - `go list ./...` returned no packages.\n' >&2
	printf 'An empty report is not a pass.\n' >&2
	cat "$workdir/list.err" >&2
	exit 1
fi

# Own-package coverage only. -coverpkg is deliberately NOT used: it attributes
# coverage across package boundaries, which would credit an untested package
# from other packages' tests and defeat hatch (b) precisely.
set +e
go test -count=1 -cover ./... 2>&1 | tee "$coverage"
test_exit="${PIPESTATUS[0]}"
set -e

printf '\n'

# Field layout is measured, not assumed. With FS="\t", $2 is the package name in
# every emitted shape:
#
#   "ok  \tPKG\tTIME\tcoverage: N% of statements"      -> tested, measured
#   "ok  \tPKG\tTIME\tcoverage: [no statements]"       -> tested, nothing coverable
#   "\tPKG\t\tcoverage: N% of statements"              -> untested but coverable (N=0.0)
#   "?   \tPKG\t[no test files]"                       -> untested, nothing coverable
#   "FAIL\tPKG\tTIME"                                  -> tests failed
#   "FAIL\tPKG [build failed]"                         -> did not build
#
# A bare "coverage: N% of statements" line is test-binary stdout with no package
# on it (NF==1) and is ignored; the owning package is classified from its own
# status line.
module_path="$(go list -m 2>/dev/null || printf '')"

set +e
awk -v floor="$floor" -v module_path="$module_path" '
	function short(p) {
		if (module_path != "" && index(p, module_path "/") == 1) {
			return substr(p, length(module_path) + 2)
		}
		if (p == module_path) return "."
		return p
	}

	FNR == NR {
		order[++total] = $0
		wanted[$0] = 1
		next
	}

	NF < 2 || $2 == "" { next }

	{
		pkg = $2
		sub(/ \[build failed\]$/, "", pkg)
		if (!(pkg in wanted)) next

		status = $1
		sub(/[ \t]+$/, "", status)

		if (status == "FAIL") {
			state[pkg] = "FAIL"
			pct[pkg] = -1
			next
		}
		if (status == "?") {
			state[pkg] = "NO_TESTS"
			pct[pkg] = 0
			next
		}

		line = $0
		if (match(line, /coverage: [0-9]+\.[0-9]+%/)) {
			value = substr(line, RSTART + 10, RLENGTH - 11)
			pct[pkg] = value + 0
			# A leading-tab row ($1 empty) is an untested package that still has
			# coverable statements. go reports it at 0.0%; name it for what it is.
			state[pkg] = (status == "" ? "NO_TESTS" : "OK")
		} else if (line ~ /coverage: \[no statements\]/) {
			state[pkg] = "NO_STATEMENTS"
			pct[pkg] = -2
		} else if (status == "ok") {
			# "ok" with no coverage token at all: unmeasured, never a pass.
			state[pkg] = "UNMEASURED"
			pct[pkg] = -1
		}
	}

	END {
		if (total == 0) {
			printf "units_measured=0\n"
			printf "\ngo-coverage-gate: FAIL - `go list ./...` returned no packages.\n"
			printf "An empty report is not a pass.\n"
			exit 1
		}

		failures = 0
		min_pct = 1e9
		min_pkg = ""

		printf "%-52s %-20s %s\n", "PACKAGE", "STATUS", "STATEMENTS"
		for (i = 1; i <= total; i++) {
			pkg = order[i]
			st = (pkg in state) ? state[pkg] : "ABSENT"
			p = (pkg in pct) ? pct[pkg] : 0

			if (st == "OK") {
				shown = sprintf("%.1f%%", p)
				if (p + 0 < floor) {
					mark = "BELOW FLOOR"
					failures++
				} else {
					mark = "ok"
				}
				if (p < min_pct) { min_pct = p; min_pkg = pkg }
			} else if (st == "NO_STATEMENTS") {
				# Tested, but the package declares nothing coverable. Legitimate:
				# there is no percentage to compare, and it is NOT counted toward
				# the reported minimum.
				shown = "[no statements]"
				mark = "ok"
			} else if (st == "NO_TESTS") {
				shown = "0.0%"
				mark = "NO TESTS"
				failures++
				if (0 < min_pct) { min_pct = 0; min_pkg = pkg }
			} else if (st == "FAIL") {
				shown = "-"
				mark = "TESTS FAILED"
				failures++
			} else if (st == "UNMEASURED") {
				shown = "-"
				mark = "UNMEASURED"
				failures++
			} else {
				# In `go list` but absent from the coverage report entirely.
				shown = "0.0%"
				mark = "ABSENT FROM REPORT"
				failures++
				if (0 < min_pct) { min_pct = 0; min_pkg = pkg }
			}
			printf "%-52s %-20s %s\n", short(pkg), mark, shown
		}

		printf "\nunits_measured=%d\n", total
		printf "floor=%d%%\n", floor
		# The reported figure is the MINIMUM across packages -- never a mean and
		# never a total. When no package reported a percentage at all, say so in
		# terms that cannot be misread as "nothing wrong".
		if (min_pkg != "") {
			printf "minimum=%.1f%% (%s)\n", min_pct, short(min_pkg)
		} else if (failures > 0) {
			printf "minimum=n/a - no package reported a percentage; %d failure(s) below\n", failures
		} else {
			printf "minimum=n/a (no package declares coverable statements)\n"
		}

		if (failures > 0) {
			printf "\ngo-coverage-gate: FAIL - %d of %d package(s) did not meet the bar.\n", failures, total
			printf "Every package must have tests, pass them, and reach %d%% of statements.\n", floor
			printf "A package with no tests is a failure, not an omission.\n"
			exit 1
		}
		printf "\ngo-coverage-gate: PASS - all %d package(s) at or above %d%%.\n", total, floor
		exit 0
	}
' "$packages" "$coverage"
gate_exit=$?
set -e

# The status token above catches a package whose tests failed. This is the
# belt-and-braces arm: a non-zero `go test` exit is a failure even if every
# package somehow reported a passing row.
if [ "$test_exit" -ne 0 ] && [ "$gate_exit" -eq 0 ]; then
	printf '\ngo-coverage-gate: FAIL - `go test` exited %s while every package reported a passing row.\n' "$test_exit" >&2
	exit 1
fi

exit "$gate_exit"
