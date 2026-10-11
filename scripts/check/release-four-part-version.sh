#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RELEASES_API_URL="fixture"

# Load the real lookup functions without executing the installer's side effects.
source <(sed -n '/^extract_release_string_field()/,/^resolve_latest_version()/p' "${ROOT_DIR}/install-release.sh" | sed '$d')
curl_with_localhost_bypass() {
  printf '%s\n' "${release_fixture}"
}

assert_latest() {
  local track="$1" expected="$2" actual
  actual="$(resolve_latest_version_from_release_api "${track}")"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "${track}: expected ${expected}, got ${actual}" >&2
    exit 1
  fi
}

# GitHub supplies publication order. A new lower-numbered fork release must
# win over an older higher-numbered upstream release.
release_fixture='[{"url":"https://api.github.com/repos/test/repo/releases/1","tag_name":"v1.0.0.1","draft":false,"prerelease":false},{"url":"https://api.github.com/repos/test/repo/releases/2","tag_name":"v2.1.2","draft":false,"prerelease":false}]'
assert_latest production v1.0.0.1
release_fixture='[{"url":"https://api.github.com/repos/test/repo/releases/1","tag_name":"v1.0.0.1-beta.2","draft":false,"prerelease":true},{"url":"https://api.github.com/repos/test/repo/releases/2","tag_name":"v1.0.0.1","draft":false,"prerelease":false}]'
assert_latest beta v1.0.0.1-beta.2
assert_latest production v1.0.0.1
release_fixture='[{"url":"https://api.github.com/repos/test/repo/releases/1","tag_name":"v1.0.0.1.2","draft":false,"prerelease":false},{"url":"https://api.github.com/repos/test/repo/releases/2","tag_name":"v2.1.2","draft":false,"prerelease":false}]'
assert_latest production v2.1.2
release_fixture='[{"url":"https://api.github.com/repos/test/repo/releases/1","tag_name":"v1.0.0.1-alpha.3","draft":false,"prerelease":true}]'
assert_latest alpha v1.0.0.1-alpha.3

source <(sed -n '/^bundle_versions_for_app()/,/^require_mac_toolchain()/p' "${ROOT_DIR}/scripts/release/build-macos-installer-app.sh" | sed '$d')
for fixture in 'v1.0.0.1|1.0.0|1.0.0' 'v1.0.0.1-beta.2|1.0.0|1.0.0b2' 'v2.1.2|2.1.2|2.1.2' 'v2.1.2-alpha.3|2.1.2|2.1.2a3'; do
  IFS='|' read -r version short_version bundle_version <<<"${fixture}"
  actual="$(bundle_versions_for_app "${version}")"
  if [[ "${actual}" != "${short_version}"$'\n'"${bundle_version}" ]]; then
    echo "macOS bundle metadata mismatch for ${version}: ${actual}" >&2
    exit 1
  fi
done

# Exercise the actual manual-release workflow validation block, including
# malformed fifth fields. GitHub expression interpolation is not needed here.
validation_block="$(sed -n '/^            case "${track}" in/,/^            latest_tag=/p' "${ROOT_DIR}/.github/workflows/release.yml" | sed '$d')"
for fixture in 'production|v1.0.0.1' 'production|v2.1.2' 'beta|v1.0.0.1-beta.2' 'alpha|v2.1.2-alpha.3'; do
  IFS='|' read -r track version <<<"${fixture}"
  (eval "${validation_block}")
done
for fixture in 'production|v1.0.0.1.2' 'beta|v1.0.0.1.2-beta.2' 'production|v1.0.0.1-beta.2'; do
  IFS='|' read -r track version <<<"${fixture}"
  if (eval "${validation_block}") >/dev/null 2>&1; then
    echo "workflow unexpectedly accepted ${version} for ${track}" >&2
    exit 1
  fi
done

notes="$(CODEX_REMOTE_REPO= GITHUB_REPOSITORY=hnfg544y7ui/codex-remote-feishu VERSION=v1.0.0.1 LATEST_TAG=HEAD bash "${ROOT_DIR}/scripts/release/release-notes.sh")"
grep -F 'https://raw.githubusercontent.com/hnfg544y7ui/codex-remote-feishu/v1.0.0.1/install-release.sh' <<<"${notes}" >/dev/null
grep -F -- '--repo hnfg544y7ui/codex-remote-feishu --version v1.0.0.1' <<<"${notes}" >/dev/null
grep -F -- '-Repo hnfg544y7ui/codex-remote-feishu -Version v1.0.0.1' <<<"${notes}" >/dev/null
if grep -F 'kxn/codex-remote-feishu' <<<"${notes}" >/dev/null; then
  echo "fork release notes must not install upstream releases" >&2
  exit 1
fi

echo "four-part installer lookup and macOS metadata selftest: ok"
