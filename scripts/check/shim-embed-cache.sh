#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
for tool in go gofmt zstd; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "shim-embed-cache: ${tool} is required" >&2
    exit 1
  fi
done

temp_parent="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
fixture_dir="$(mktemp -d "${temp_parent}/shim-embed-cache.XXXXXX")"
fixture_dir="$(cd "${fixture_dir}" && pwd -P)"
case "${fixture_dir}" in
  "${temp_parent}"/shim-embed-cache.*) ;;
  *) echo "shim-embed-cache: unexpected temporary path" >&2; exit 1 ;;
esac
cleanup() {
  # Delete only the resolved mktemp fixture, never a computed repository path.
  case "${fixture_dir}" in
    "${temp_parent}"/shim-embed-cache.*) rm -rf -- "${fixture_dir}" ;;
  esac
}
trap cleanup EXIT

checksum_cmd=(sha256sum)
if ! command -v "${checksum_cmd[0]}" >/dev/null 2>&1; then
  checksum_cmd=(shasum -a 256)
fi
checksum() {
  "${checksum_cmd[@]}" "$1" | awk '{print $1}'
}

mkdir -p "${fixture_dir}/scripts/shim" "${fixture_dir}/cmd/shim" \
  "${fixture_dir}/internal/facade" "${fixture_dir}/internal/app/install"
cp "${ROOT_DIR}/scripts/shim/prepare-shim-embed.sh" "${fixture_dir}/scripts/shim/"
cat > "${fixture_dir}/go.mod" <<'EOF'
module example.com/shim-cache-fixture

go 1.23
EOF
: > "${fixture_dir}/go.sum"
cat > "${fixture_dir}/cmd/shim/main.go" <<'EOF'
package main

import (
    "fmt"
    "example.com/shim-cache-fixture/internal/facade"
)

func main() { fmt.Println(facade.Token()) }
EOF
cat > "${fixture_dir}/internal/facade/token.go" <<'EOF'
package facade

import "example.com/shim-cache-fixture/internal/app/install"

func Token() string { return install.Token() }
EOF
write_install_token() {
  printf 'package install\n\nfunc Token() string { return "%s" }\n' "$1" \
    > "${fixture_dir}/internal/app/install/token.go"
}
write_install_token old-install-token

# Use the installed toolchain, no workspace, proxy or dependency downloads.
export GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
asset="${fixture_dir}/internal/shim/embed/assets/codex-remote-shim-${host_os}-${host_arch}.zst"
decoded="${fixture_dir}/decoded-shim"
if [[ "${host_os}" == windows ]]; then
  decoded="${decoded}.exe"
fi

prepare() {
  if ! (cd "${fixture_dir}" && bash scripts/shim/prepare-shim-embed.sh "${host_os}" "${host_arch}") \
    > "${fixture_dir}/prepare.log" 2>&1; then
    cat "${fixture_dir}/prepare.log" >&2
    echo "shim-embed-cache: preparation failed" >&2
    exit 1
  fi
}
require_log() {
  if ! grep -Fq "$1" "${fixture_dir}/prepare.log"; then
    cat "${fixture_dir}/prepare.log" >&2
    echo "shim-embed-cache: expected ${1}" >&2
    exit 1
  fi
}
require_token() {
  zstd -d -q -f -o "${decoded}" "${asset}"
  chmod +x "${decoded}"
  local actual
  actual="$("${decoded}")"
  if [[ "${actual}" != "$1" ]]; then
    echo "shim-embed-cache: executable emitted ${actual}, expected $1" >&2
    exit 1
  fi
}

prepare
require_log "prepared shim asset"
require_token old-install-token
old_sha="$(checksum "${asset}")"
prepare
require_log "shim asset already up to date"
[[ "$(checksum "${asset}")" == "${old_sha}" ]]

# Neither cmd/shim nor internal/shim changes: only an imported dependency does.
write_install_token new-install-token
prepare
require_log "prepared shim asset"
require_token new-install-token
new_sha="$(checksum "${asset}")"
if [[ "${new_sha}" == "${old_sha}" ]]; then
  echo "shim-embed-cache: transitive install change reused the old asset" >&2
  exit 1
fi
prepare
require_log "shim asset already up to date"
[[ "$(checksum "${asset}")" == "${new_sha}" ]]
echo "shim-embed-cache: passed (build, hit, dependency rebuild, hit)"
