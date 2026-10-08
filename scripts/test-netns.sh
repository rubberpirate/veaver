#!/bin/sh
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
export LINKWEAVE_NETNS_TEST=1
exec unshare --user --map-root-user --net go test -p 1 -count=1 -v ./internal/platform ./tests/integration -run 'TestNamespaceRoutingIntegration|TestRealBalanceAndTransparentRelay'
