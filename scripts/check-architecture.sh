#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

violations="$(cd "${REPO_ROOT}" && rg -n 'github\.com/yuri/y/internal/' pkg --glob '*.go' || true)"
if [ -n "${violations}" ]; then
	echo "check-architecture: public packages must not import top-level internal packages:" >&2
	echo "${violations}" >&2
	exit 1
fi

echo "check-architecture: public package boundaries clean"
