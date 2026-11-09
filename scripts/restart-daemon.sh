#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v go >/dev/null 2>&1; then
	for go_bin in "${HOME}/.local/opt/go/bin" "/usr/local/go/bin"; do
		if [[ -x "${go_bin}/go" ]]; then
			export PATH="${go_bin}:${PATH}"
			break
		fi
	done
fi

if ! command -v go >/dev/null 2>&1; then
	printf 'error: go toolchain not found\n' >&2
	exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
	printf 'error: systemctl not found\n' >&2
	exit 1
fi
if ! systemctl --user cat dsmon.service >/dev/null 2>&1; then
	printf 'error: user unit dsmon.service is not installed\n' >&2
	exit 1
fi

printf 'Building dsmon...\n'
make build

printf 'Installing %s/.local/bin/dsmon...\n' "$HOME"
install -Dm755 build/dsmon "$HOME/.local/bin/dsmon"

printf 'Restarting dsmon.service...\n'
systemctl --user daemon-reload
systemctl --user restart dsmon.service
systemctl --user --no-pager --full status dsmon.service
