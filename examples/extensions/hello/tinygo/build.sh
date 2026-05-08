#!/usr/bin/env bash
# Build the Hello example extension into a WASM module that the y host can
# load. Requires TinyGo (https://tinygo.org). The output is written next to
# the manifest so `y extension list --dir ../` discovers it.

set -euo pipefail

dir=$(cd "$(dirname "$0")" && pwd)
extension_dir=$(cd "$dir/.." && pwd)

if ! command -v tinygo >/dev/null 2>&1; then
    echo "tinygo not found in PATH; install from https://tinygo.org" >&2
    exit 1
fi

tinygo build \
    -target=wasi \
    -opt=z \
    -no-debug \
    -o "$extension_dir/module.wasm" \
    "$dir"

echo "wrote $extension_dir/module.wasm"
