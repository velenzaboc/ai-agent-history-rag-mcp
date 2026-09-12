#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workflow_path="${1:-${repo_root}/.github/workflows/ci.yml}"

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

expected_group="  group: ci-\${{ github.workflow }}-\${{ github.event_name == 'push' && github.sha || github.ref }}"
expected_cancel="  cancel-in-progress: \${{ github.event_name != 'push' }}"
expected_block=$(printf 'concurrency:\n%s\n%s' "$expected_group" "$expected_cancel")

validate_concurrency() {
  local source=$1
  local count

  [[ "$source" == *"$expected_block"* ]] || return 1

  count=$(grep -c '^concurrency:$' <<<"$source" || true)
  [[ "$count" == 1 ]] || return 1
  count=$(grep -c '^  group:' <<<"$source" || true)
  [[ "$count" == 1 ]] || return 1
  count=$(grep -c '^  cancel-in-progress:' <<<"$source" || true)
  [[ "$count" == 1 ]] || return 1
  count=$(grep -Fxc "$expected_group" <<<"$source" || true)
  [[ "$count" == 1 ]] || return 1
  count=$(grep -Fxc "$expected_cancel" <<<"$source" || true)
  [[ "$count" == 1 ]] || return 1
}

assert_rejected() {
  local name=$1
  local source=$2
  if validate_concurrency "$source"; then
    fail "mutation survived: ${name}"
  fi
  printf 'PASS mutation killed: %s\n' "$name"
}

[[ -f "$workflow_path" ]] || fail "workflow is unavailable: ${workflow_path}"
workflow=$(<"$workflow_path")
validate_concurrency "$workflow" || fail 'main-push verdicts must be SHA-unique while non-push runs remain ref-stable and cancellable'
printf 'PASS checked-in concurrency policy\n'

collision_group="  group: ci-\${{ github.workflow }}-\${{ github.ref }}"
sha_only_group="  group: ci-\${{ github.workflow }}-\${{ github.sha }}"
always_cancel='  cancel-in-progress: true'
never_cancel='  cancel-in-progress: false'

mutated=${workflow/"$expected_group"/"$collision_group"}
[[ "$mutated" != "$workflow" ]] || fail 'push-collision mutation was not applied'
assert_rejected 'pushes collide by ref' "$mutated"

mutated=${workflow/"$expected_group"/"$sha_only_group"}
[[ "$mutated" != "$workflow" ]] || fail 'non-push-identity mutation was not applied'
assert_rejected 'non-push runs lose ref identity' "$mutated"

mutated=${workflow/"$expected_cancel"/"$always_cancel"}
[[ "$mutated" != "$workflow" ]] || fail 'push-cancellation mutation was not applied'
assert_rejected 'main-push verdict can be cancelled' "$mutated"

mutated=${workflow/"$expected_cancel"/"$never_cancel"}
[[ "$mutated" != "$workflow" ]] || fail 'non-push-cancellation mutation was not applied'
assert_rejected 'superseded non-push run cannot cancel' "$mutated"

printf 'PASS total=5\n'
