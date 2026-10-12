#!/usr/bin/env bash
set -euo pipefail
SOURCE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/safe-push.sh"
temp_root="$(mktemp -d "${TMPDIR:-/tmp}/safe-push-test.XXXXXX")"
temp_root="$(cd "${temp_root}" && pwd -P)"
[[ "${temp_root}" == /*/safe-push-test.* ]] || exit 1
trap 'rm -rf "${temp_root}"' EXIT
git init --bare -q "${temp_root}/remote.git"
git init -q -b feature-test "${temp_root}/repo"
cd "${temp_root}/repo"
mkdir -p scripts/git
cp "${SOURCE}" scripts/git/safe-push.sh
printf 'test\n' > fixture.txt
git add .
git -c user.name=Test -c user.email=test@example.invalid commit -qm initial
git remote add origin "${temp_root}/remote.git"
mkdir -p .git/codex-remote
git rev-parse 'HEAD^{tree}' > .git/codex-remote/pre-commit-tree
bash scripts/git/safe-push.sh --create-branch > "${temp_root}/first.log" 2>&1
test "$(git rev-parse '@{upstream}')" = "$(git rev-parse HEAD)"
test "$(git --git-dir="${temp_root}/remote.git" rev-parse refs/heads/feature-test)" = "$(git rev-parse HEAD)"
# Existing branches still take the regular synchronization path.
bash scripts/git/safe-push.sh --create-branch > "${temp_root}/existing.log" 2>&1
grep -q 'remote is not ahead' "${temp_root}/existing.log"
# A deleted tracking branch must not be recreated without an explicit decision.
git --git-dir="${temp_root}/remote.git" update-ref -d refs/heads/feature-test
if bash scripts/git/safe-push.sh --create-branch > "${temp_root}/deleted.log" 2>&1; then
  echo 'deleted tracked branch was recreated' >&2; exit 1
fi
grep -q 'without an upstream' "${temp_root}/deleted.log"
# Remote query errors must never be treated as an absent ref.
git branch --unset-upstream
git remote set-url origin "${temp_root}/missing.git"
if bash scripts/git/safe-push.sh --create-branch > "${temp_root}/offline.log" 2>&1; then
  echo 'invalid remote was accepted' >&2; exit 1
fi
grep -q 'cannot verify remote branch existence' "${temp_root}/offline.log"
# The empty expected-ref lease rejects a ref created after the existence probe.
original_tip="$(git rev-parse HEAD)"
git --git-dir="${temp_root}/remote.git" update-ref refs/heads/feature-test "${original_tip}"
printf 'second\n' >> fixture.txt
git add fixture.txt
git -c user.name=Test -c user.email=test@example.invalid commit -qm second
if git push --force-with-lease=refs/heads/feature-test: "${temp_root}/remote.git" HEAD:refs/heads/feature-test > "${temp_root}/race.log" 2>&1; then
  echo 'concurrently created ref was overwritten' >&2; exit 1
fi
grep -q 'stale info' "${temp_root}/race.log"
test "$(git --git-dir="${temp_root}/remote.git" rev-parse refs/heads/feature-test)" = "${original_tip}"
echo 'safe-push first publication selftest: passed'
