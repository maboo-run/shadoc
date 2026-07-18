.PHONY: test test-release-scripts build release verify-release-version verify-release-artifacts web test-e2e verify-release
VERSION ?= 0.1.0
web:
	pnpm --filter shadoc-web build
test-release-scripts:
	sh -n scripts/install.sh
	sh -n scripts/validate-release-version.sh
	sh -n scripts/test-public-export.sh
	./scripts/test-release-version.sh
	./scripts/test-install.sh
	./scripts/test-public-export.sh
test: test-release-scripts
	go test ./...
	go vet ./...
	pnpm --filter shadoc-web test --run
	pnpm --filter shadoc-web build
build: web
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc ./cmd/restic-control
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-linux-amd64 ./cmd/restic-control-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-linux-arm64 ./cmd/restic-control-agent
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-darwin-amd64 ./cmd/restic-control-agent
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-darwin-arm64 ./cmd/restic-control-agent
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-windows-amd64.exe ./cmd/restic-control-agent
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc-agent-windows-arm64.exe ./cmd/restic-control-agent
	cp dist/shadoc-agent-$(shell go env GOOS)-$(shell go env GOARCH) dist/shadoc-agent
verify-release-version:
	./scripts/validate-release-version.sh "$(VERSION)"
release: verify-release-version build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc_linux_amd64 ./cmd/restic-control
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc_linux_arm64 ./cmd/restic-control
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc_darwin_amd64 ./cmd/restic-control
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.applicationVersion=$(VERSION)" -o dist/shadoc_darwin_arm64 ./cmd/restic-control
	cp scripts/install.sh dist/install.sh
	cp LICENSE dist/LICENSE
	cd dist && shasum -a 256 shadoc_linux_amd64 shadoc_linux_arm64 shadoc_darwin_amd64 shadoc_darwin_arm64 shadoc-agent-linux-amd64 shadoc-agent-linux-arm64 shadoc-agent-darwin-amd64 shadoc-agent-darwin-arm64 shadoc-agent-windows-amd64.exe shadoc-agent-windows-arm64.exe install.sh LICENSE > SHA256SUMS
	$(MAKE) verify-release-artifacts
verify-release-artifacts:
	cd dist && shasum -a 256 -c SHA256SUMS
test-e2e: build
	go test -tags=e2e ./tests/e2e -v
verify-release: test build
	SHADOC_RELEASE_VERIFY=1 SHADOC_E2E_REPORT=$(CURDIR)/dist/e2e-report.json go test -tags=e2e ./tests/e2e -v
