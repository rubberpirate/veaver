#!/bin/sh
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
mkdir -p build
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o build/veaver ./cmd/veaver
echo 'Built build/veaver (dashboard assets included; no frontend runtime needed).'
