#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
version_file="${DSMON_VERSION_FILE:-${repo_root}/internal/version/VERSION}"

if [[ ! -f "$version_file" ]]; then
	printf 'error: version file not found: %s\n' "$version_file" >&2
	exit 1
fi

current="$(tr -d '[:space:]' <"$version_file")"
if [[ ! "$current" =~ ^([0-9]{1,2})\.([0-9]{1,2})\.([0-9]{1,2})$ ]]; then
	printf 'error: invalid version %q; expected major.minor.patch with components 0–99\n' "$current" >&2
	exit 1
fi

major=$((10#${BASH_REMATCH[1]}))
minor=$((10#${BASH_REMATCH[2]}))
patch=$((10#${BASH_REMATCH[3]}))

if ((patch < 99)); then
	patch=$((patch + 1))
elif ((minor < 99)); then
	minor=$((minor + 1))
	patch=0
elif ((major < 99)); then
	major=$((major + 1))
	minor=0
	patch=0
else
	printf 'error: version %s is at the maximum 99.99.99\n' "$current" >&2
	exit 1
fi

next="${major}.${minor}.${patch}"
temp_file="${version_file}.tmp.$$"
trap 'rm -f "$temp_file"' EXIT
printf '%s\n' "$next" >"$temp_file"
mv "$temp_file" "$version_file"
trap - EXIT

printf 'version %s -> %s\n' "$current" "$next"
