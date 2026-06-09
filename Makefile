# k8tc — build & test
#
# A fresh checkout + `make` produces the static binary. Module dependencies are
# fetched automatically by the Go toolchain from go.mod / go.sum.

BINARY  := k8tc
PKG     := ./cmd/k8tc
GO      ?= go

# Static, reproducible build (no cgo, trimmed paths). Override at the CLI, e.g.
#   make build GOOS=darwin GOARCH=arm64
export CGO_ENABLED := 0
BUILD_FLAGS := -trimpath

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the static binary (default)
	$(GO) build $(BUILD_FLAGS) -o $(BINARY) $(PKG)

.PHONY: test
test: ## Run all tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go source in place
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: check
check: fmt-check vet test ## Run fmt-check, vet and tests

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: install
install: ## Install the binary into $GOBIN / $GOPATH/bin
	$(GO) install $(BUILD_FLAGS) $(PKG)

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
	$(GO) clean

.PHONY: help
help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
