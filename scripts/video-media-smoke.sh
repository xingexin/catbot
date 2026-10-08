#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/secretary-video-test.XXXXXX")
bundle="$scratch/test.cjs"
trap 'rm -f "$bundle"; rmdir "$scratch"' EXIT HUP INT TERM
./node_modules/.bin/esbuild plugins/video/test/ffmpeg.test.ts --bundle --platform=node --format=cjs --outfile="$bundle"
./scripts/compose exec -T -e REQUIRE_FFMPEG=1 backend node - < "$bundle"
