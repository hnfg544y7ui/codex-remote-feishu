#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

escape_regex() {
  printf '%s' "$1" | sed -e 's/[.[\*^$()+?{|\\]/\\&/g'
}

repo_root_pattern="$(escape_regex "${ROOT_DIR}")"
repo_parent="$(dirname "${ROOT_DIR}")"
repo_parent_pattern="$(escape_regex "${repo_parent}")"
home_pattern=""
if [[ -n "${HOME:-}" ]]; then
  home_pattern="$(escape_regex "${HOME}")"
fi

patterns=(
  "${repo_root_pattern}"
  "${repo_parent_pattern}/fschannel[[:alnum:]_-]*"
  '(^|[^[:alnum:]_])/(home|Users)/[^/"'"'"'[:space:]]+/'
  '(^|[^[:alnum:]_])/private/var/folders/[^/"'"'"'[:space:]]+/[^/"'"'"'[:space:]]+/'
  '(^|[^[:alnum:]_])[A-Za-z]:\\\\Users\\\\[^\\/"'"'"'[:space:]]+\\\\'
)
if [[ -n "${home_pattern}" ]]; then
  patterns+=("${home_pattern}")
fi

files=()
while IFS= read -r file; do
  case "${file}" in
    scripts/check/no-local-paths.sh|scripts/check/no-legacy-names.sh)
      continue
      ;;
  esac
  files+=("${file}")
done < <(
  git ls-files -- \
    '*.go' '*.md' '*.yml' '*.yaml' '*.json' '*.sh' '*.ps1' \
    '*.ts' '*.tsx' '*.js' '*.jsx' '*.css' '*.html' '*.txt'
)

if [[ ${#files[@]} -eq 0 ]]; then
  exit 0
fi

allowlisted_demo_pattern='/(home|Users)/(demo|example|sample|test)(/|$)'

search_args=()
if command -v rg >/dev/null 2>&1; then
  for pattern in "${patterns[@]}"; do
    search_args+=(-e "${pattern}")
  done
  search_command=(env MSYS2_ARG_CONV_EXCL='*' rg -n --path-separator / "${search_args[@]}" --)
else
  combined_pattern="$(IFS='|'; printf '%s' "${patterns[*]}")"
  search_command=(grep -nE "${combined_pattern}" --)
fi

# Keep native Windows command lines bounded while checking every tracked file.
# A search error must fail the guard rather than being treated as no matches.
search_tracked_files() {
  local offset status
  for ((offset = 0; offset < ${#files[@]}; offset += 64)); do
    if "${search_command[@]}" "${files[@]:offset:64}"; then
      continue
    else
      status=$?
      if [[ "${status}" -ne 1 ]]; then
        return "${status}"
      fi
    fi
  done
  return 0
}
raw_matches="$(search_tracked_files)"

filtered_matches="$(printf '%s\n' "${raw_matches}" | grep -vE "${allowlisted_demo_pattern}" || true)"
filtered_matches="$(printf '%s\n' "${filtered_matches}" | sed '/^$/d')"

if [[ -n "${filtered_matches}" ]]; then
  printf '%s\n' "${filtered_matches}"
  echo "Found machine-local absolute paths in tracked source or docs." >&2
  exit 1
fi
