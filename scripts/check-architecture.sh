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

# Core runtime packages may depend on generic extension contracts, but must not
# select or implement the memory extension. Built-in extension wiring belongs
# in an application composition root.
core_violations="$(cd "${REPO_ROOT}" && rg -n 'github\.com/yuri/y/(pkg/extensions/memory|internal/memory)' \
	pkg/agent pkg/context pkg/tools pkg/config internal/feature internal/runtime internal/config \
	--glob '*.go' || true)"
if [ -n "${core_violations}" ]; then
	echo "check-architecture: core packages must not import the memory extension or adapter:" >&2
	echo "${core_violations}" >&2
	exit 1
fi

echo "check-architecture: public package boundaries clean"
