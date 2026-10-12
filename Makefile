VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
REGISTRY  ?= ghcr.io/leon2003m
BUILD_ARGS = --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION)

.PHONY: help fmt fmt-check vet staticcheck test cover lint check build scan clean

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

fmt: ## Format Go code
	gofmt -w .

fmt-check: ## Fail if Go code is not formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet: ## go vet
	go vet ./...

staticcheck: ## staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
	staticcheck ./...

test: ## Unit and in-process integration tests with the race detector
	go test -race -count=1 ./...

cover: ## Tests with coverage report
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint: fmt-check vet staticcheck ## Go linters
	shellcheck deploy/firewall.sh deploy/dns-forwarder/entrypoint.sh test/integration/*.sh test/integration/oidc/*.sh
	hadolint Dockerfile image/Dockerfile deploy/dns-forwarder/Dockerfile
	actionlint

check: lint test ## Everything CI checks on code and config

build: ## Build all images locally
	docker build $(BUILD_ARGS) --target gateway   -t $(REGISTRY)/termiplus-gateway:$(VERSION) .
	docker build $(BUILD_ARGS) --target sockproxy -t $(REGISTRY)/termiplus-sockproxy:$(VERSION) .
	docker build $(BUILD_ARGS) --target mcp       -t $(REGISTRY)/termiplus-mcp:$(VERSION) .
	docker build $(BUILD_ARGS) -t $(REGISTRY)/termiplus-terminal:$(VERSION) image/

scan: ## Secret and vulnerability scans (needs gitleaks, govulncheck, trivy)
	gitleaks detect --redact --config .gitleaks.toml
	govulncheck ./...
	trivy fs --scanners vuln,misconfig,secret --severity HIGH,CRITICAL .

clean: ## Remove local build output
	rm -rf bin dist reports coverage.out
