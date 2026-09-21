# ===========================================================================
# shhgit — build, test and run targets
#
# The product is a single static Go binary built from cmd/shhgit. The dashboard
# is embedded from cmd/shhgit/dashboard/index.html; there is no build step for
# it and no JavaScript toolchain to install.
#
# The recipes use a POSIX shell. That is what make picks on Linux and macOS, and
# on Windows it picks the sh.exe that ships with Git for Windows — which shhgit
# needs anyway to clone repositories. Windows users who do not have GNU make (or
# a POSIX shell) should use the native PowerShell scripts instead:
#
#   .\install.ps1        one-shot installer (mirrors install.sh)
#   .\build.ps1          build for this platform
#   .\build.ps1 -All     cross-compile all six OS/arch targets into dist\
#   .\run.ps1            run the dashboard
#
# CGO is turned off through make's own environment export, so no `VAR=value cmd`
# shell prefix is used anywhere in the recipes.
# ===========================================================================

BINARY  := shhgit
MAIN    := ./cmd/shhgit
DIST    := dist
LDFLAGS := -s -w
GOEXE   := $(shell go env GOEXE)

# Export CGO_ENABLED to every recipe. `export` puts the value in the child
# process environment; test-race overrides it for its own recipe only.
export CGO_ENABLED := 0

# build-all passes GOOS/GOARCH to a sub-make on the command line. Export them
# only when set, so a host build never sees an empty GOOS.
ifneq ($(GOOS),)
export GOOS
endif
ifneq ($(GOARCH),)
export GOARCH
endif

# .exe suffix for a cross target, keyed off the GOOS the sub-make was given.
ifeq ($(GOOS),windows)
  OUTEXT := .exe
else
  OUTEXT :=
endif

.DEFAULT_GOAL := help
.PHONY: help build build-one build-all test test-race race-run vet fmt fmt-check \
        tidy clean run run-web run-tui run-terminal run-scanner scan test-tokens \
        docker-build docker-up docker-down docker-logs

help:
	@echo shhgit targets:
	@echo   build            Build the shhgit binary for this platform
	@echo   build-all        Cross-compile Windows, Linux and macOS, amd64 and arm64, into dist/
	@echo   test             Run the test suite
	@echo   test-race        Run the test suite under the race detector
	@echo   vet              Run go vet
	@echo   fmt              Format all Go source
	@echo   fmt-check        Fail if any Go file is unformatted
	@echo   tidy             Tidy go.mod / go.sum
	@echo   clean            Remove build output and test caches
	@echo   run-web          Run the web dashboard on 127.0.0.1:8080
	@echo   run-tui          Run the interactive terminal UI
	@echo   run-terminal     Run the plain terminal live match feed
	@echo   run-scanner      Run scanner-only mode, an alias for run-terminal
	@echo   scan DIR=...     Scan a local directory
	@echo   test-tokens      Validate AI provider tokens from config.yaml
	@echo   docker-build     Build the Docker image
	@echo   docker-up        Start the Docker stack in the background
	@echo   docker-down      Stop the Docker stack
	@echo   docker-logs      Follow Docker stack logs

# --- Build -----------------------------------------------------------------

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o "$(BINARY)$(GOEXE)" $(MAIN)

build-all:
	@mkdir -p "$(DIST)"
	@$(MAKE) --no-print-directory build-one GOOS=linux   GOARCH=amd64
	@$(MAKE) --no-print-directory build-one GOOS=linux   GOARCH=arm64
	@$(MAKE) --no-print-directory build-one GOOS=darwin  GOARCH=amd64
	@$(MAKE) --no-print-directory build-one GOOS=darwin  GOARCH=arm64
	@$(MAKE) --no-print-directory build-one GOOS=windows GOARCH=amd64
	@$(MAKE) --no-print-directory build-one GOOS=windows GOARCH=arm64
	@echo cross-compiled 6 targets into $(DIST)/

build-one:
	go build -trimpath -ldflags="$(LDFLAGS)" -o "$(DIST)/$(BINARY)-$(GOOS)-$(GOARCH)$(OUTEXT)" $(MAIN)
	@echo   $(DIST)/$(BINARY)-$(GOOS)-$(GOARCH)$(OUTEXT)

# --- Quality ---------------------------------------------------------------

test:
	go test ./... -count=1

# The race detector needs cgo. The sub-make re-reads this file, and its
# command-line CGO_ENABLED=1 wins over the exported default above.
test-race:
	@$(MAKE) --no-print-directory race-run CGO_ENABLED=1

race-run:
	go test ./... -count=1 -race

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "unformatted files:"; echo "$$files"; exit 1; fi; echo all Go files are gofmt-clean

tidy:
	go mod tidy

clean:
	-@rm -rf "$(DIST)" "$(BINARY)$(GOEXE)" run.log
	@go clean -testcache

# --- Run -------------------------------------------------------------------

run: run-web

run-web:
	@./$(BINARY)$(GOEXE) --web --web-host 127.0.0.1 --web-port 8080 --config-path .

run-tui:
	@./$(BINARY)$(GOEXE) --tui --config-path .

run-terminal:
	@./$(BINARY)$(GOEXE) --terminal --config-path .

run-scanner:
	@./$(BINARY)$(GOEXE) --scanner --config-path .

scan:
	@test -n "$(DIR)" || (echo "usage: make scan DIR=/path/to/code"; exit 1)
	@./$(BINARY)$(GOEXE) --local "$(DIR)" --config-path .

test-tokens:
	@./$(BINARY)$(GOEXE) --test-tokens --config-path .

# --- Docker ----------------------------------------------------------------

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
