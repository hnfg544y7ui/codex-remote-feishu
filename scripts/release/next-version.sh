#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="${ROOT_DIR_OVERRIDE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "${ROOT_DIR}"

track="${RELEASE_TRACK:-production}"
bump="${BUMP_OVERRIDE:-auto}"
case "${track}" in
  production|beta|alpha) ;;
  *)
    echo "Unsupported release track: ${track}" >&2
    exit 1
    ;;
esac

case "${bump}" in
  auto|patch|minor|major) ;;
  *)
    echo "Unsupported bump override: ${bump}" >&2
    exit 1
    ;;
esac

latest_production_tag="$(
  git tag --list 'v*' --sort=-version:refname |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?$' |
    head -n1 ||
    true
)"

if [[ -z "${latest_production_tag}" ]]; then
  major=0
  minor=0
  patch=0
  if [[ "${bump}" == "auto" ]]; then
    bump="minor"
  fi
else
  version_core="${latest_production_tag#v}"
  # The optional revision identifies a maintenance release. Automatic major,
  # minor, and patch bumps keep their existing three-part output, but use the
  # latest maintenance tag as the commit-range boundary.
  IFS=. read -r major minor patch revision <<<"${version_core}"

  if [[ -z "${major:-}" || -z "${minor:-}" || -z "${patch:-}" ]]; then
    echo "Latest production tag is not a semantic version: ${latest_production_tag}" >&2
    exit 1
  fi
fi

if [[ -n "${latest_production_tag}" ]]; then
  commit_range="${latest_production_tag}..HEAD"
  if [[ -z "$(git log --format='%H' "${commit_range}")" ]]; then
    echo "No unreleased commits since ${latest_production_tag}." >&2
    exit 1
  fi
fi

if [[ "${bump}" == "auto" ]]; then
  if [[ -n "${latest_production_tag}" ]]; then
    subjects="$(git log --format='%s' "${commit_range}")"
    bodies="$(git log --format='%B' "${commit_range}")"
  else
    subjects="$(git log --format='%s')"
    bodies="$(git log --format='%B')"
  fi

  if grep -Eq 'BREAKING CHANGE|^[^[:space:]]+(\(.+\))?!:' <<<"${bodies}"$'\n'"${subjects}"; then
    bump="major"
  elif grep -Eq '^(feat(\(.+\))?:|Add |Implement |Create |Support )' <<<"${subjects}"; then
    bump="minor"
  else
    bump="patch"
  fi
fi

case "${bump}" in
  major)
    major=$((major + 1))
    minor=0
    patch=0
    ;;
  minor)
    minor=$((minor + 1))
    patch=0
    ;;
  patch)
    patch=$((patch + 1))
    ;;
esac

if [[ "${track}" == "production" ]]; then
  printf 'v%s.%s.%s\n' "${major}" "${minor}" "${patch}"
  exit 0
fi

prerelease_tag_regex="^v${major}\\.${minor}\\.${patch}-${track}\\.[0-9]+$"
latest_track_tag="$(
  git tag --list "v${major}.${minor}.${patch}-${track}.*" --sort=-version:refname |
    grep -E "${prerelease_tag_regex}" |
    head -n1 ||
    true
)"

sequence=1
if [[ -n "${latest_track_tag}" ]]; then
  sequence="${latest_track_tag##*.}"
  sequence=$((sequence + 1))
fi

printf 'v%s.%s.%s-%s.%s\n' "${major}" "${minor}" "${patch}" "${track}" "${sequence}"
