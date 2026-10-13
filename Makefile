.PHONY: build test vet check release
build:
	./scripts/build.sh
test:
	go test -race ./...
vet:
	go vet ./...
check: test vet
	node --check internal/web/assets/app.js
	node --check scripts/test-dashboard.cjs
	python3 -c 'import ast, pathlib; ast.parse(pathlib.Path("scripts/test-tui.py").read_text())'
	for script in scripts/*.sh deploy/90-linkweave; do sh -n "$$script" || exit; done
release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o dist/linkweave-linux-amd64 ./cmd/linkweave
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o dist/linkweave-linux-arm64 ./cmd/linkweave
