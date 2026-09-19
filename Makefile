# ===========================================================================
# shhgit — build, test and run targets
#
# The product is a single static Go binary built from the repo root.
# `server/` is a legacy standalone module and is NOT part of the build.
# ===========================================================================

SHELL    := /bin/sh
BINARY   := shhgit
GOEXE    := $(shell go env GOEXE)
DIST     := dist
PKG      := github.com/trebor048/shhgot
LDFLAGS  := -s -w
GOFLAGS  := CGO_ENABLED=0

.DEFAULT_GOAL := help
.PHONY: help build build-all test test-race vet fmt fmt-check tidy clean \
        run run-web run-tui run-scanner scan test-tokens \
        frontend docker-build docker-up docker-down docker-logs

help: ## Show this help
	@echo "shhgit targets:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""

# --- Build -----------------------------------------------------------------

build: ## Build the shhgit binary for this platform
	$(GOFLAGS) go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)$(GOEXE) .

build-all: ## Cross-compile Windows, Linux and macOS (amd64 + arm64) into dist/
	@mkdir -p $(DIST)
	@set -e; \
	for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
			out="$(DIST)/$(BINARY)-$$os-$$arch$$ext"; \
			echo "  building $$out"; \
			$(GOFLAGS) GOOS=$$os GOARCH=$$arch go build -trimpath \
				-ldflags="$(LDFLAGS)" -o "$$out" . ; \
		done; \
	done
	@echo ""
	@ls -lh $(DIST)

# --- Quality ---------------------------------------------------------------

test: ## Run the test suite
	go test ./... -count=1

test-race: ## Run the test suite with the race detector
	CGO_ENABLED=1 go test ./... -count=1 -race

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go source
	gofmt -w .

fmt-check: ## Fail if any Go file is unformatted (used by CI)
	@files=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$files" ]; then echo "unformatted files:"; echo "$$files"; exit 1; fi

tidy: ## Tidy go.mod / go.sum
	go mod tidy

clean: ## Remove build output and test caches
	rm -rf $(DIST) $(BINARY) $(BINARY).exe shhgit-server run.log
	go clean -testcache

# --- Run -------------------------------------------------------------------

run: run-web ## Alias for run-web

run-web: ## Run the web dashboard on 127.0.0.1:8080
	./$(BINARY)$(GOEXE) --web --web-host 127.0.0.1 --web-port 8080 --config-path .

run-tui: ## Run the terminal UI (same as running with no flags)
	./$(BINARY)$(GOEXE) --tui --config-path .

run-scanner: ## Run scanner-only mode (no UI)
	./$(BINARY)$(GOEXE) --scanner --config-path .

scan: ## Scan a local directory, e.g. make scan DIR=/path/to/code
	@test -n "$(DIR)" || (echo "usage: make scan DIR=/path/to/code" && exit 1)
	./$(BINARY)$(GOEXE) --local "$(DIR)" --config-path .

test-tokens: ## Validate AI provider tokens from config.yaml (no scanning)
	./$(BINARY)$(GOEXE) --test-tokens --config-path .

# --- Frontend (legacy, optional) -------------------------------------------

frontend: ## Build the legacy React dashboard into frontend/dist
	cd frontend && npm install && npm run build

# --- Docker ----------------------------------------------------------------

docker-build: ## Build the Docker image
	docker compose build

docker-up: ## Start the Docker stack in the background
	docker compose up -d

docker-down: ## Stop the Docker stack
	docker compose down

docker-logs: ## Follow Docker stack logs
	docker compose logs -f
