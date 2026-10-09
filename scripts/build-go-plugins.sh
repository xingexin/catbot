#!/bin/sh
set -eu
root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"
# Build the helper for this machine, then apply cross-compilation to plugins only.
target_os=${GOOS:-}
target_arch=${GOARCH:-}
exec env -u GOOS -u GOARCH go run ./scripts/build-go-plugins -os "$target_os" -arch "$target_arch" "$@"
