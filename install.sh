#!/usr/bin/env bash
#
# shhgit — one-shot installer for Linux and macOS
# ==============================================
# Checks/installs Go, gets the source, builds the shhgit binary and creates
# config.yaml from the example. No Docker required.
#
#   ./install.sh                 # install into ./ (or clone into $HOME/shhgit)
#   INSTALL_DIR=~/tools/shhgit ./install.sh
#   ./install.sh --no-build      # set up source + config only
#   ./install.sh --docker        # print the Docker route instead
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/trebor048/shhgot/main/install.sh | bash
#
set -euo pipefail

REPO_URL="https://github.com/trebor048/shhgot.git"
REPO_SLUG="trebor048/shhgot"
INSTALL_DIR="${INSTALL_DIR:-$HOME/shhgit}"
BINARY="shhgit"
DO_BUILD=1
DOCKER_MODE=0

# Fallback if go.mod cannot be read; keep in sync with the `go` directive.
FALLBACK_GO_VERSION="1.26.0"

# ── Output helpers ─────────────────────────────────────────────────────────
if [ -t 1 ]; then
    BOLD='\033[1m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
    RED='\033[0;31m'; BLUE='\033[0;34m'; NC='\033[0m'
else
    BOLD=''; GREEN=''; YELLOW=''; RED=''; BLUE=''; NC=''
fi

log()  { printf "${GREEN}[ok]${NC} %s\n" "$1"; }
info() { printf "${BLUE}[..]${NC} %s\n" "$1"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$1"; }
fail() { printf "${RED}[x]${NC} %s\n" "$1" >&2; exit 1; }
step() { printf "\n${BOLD}%s${NC}\n" "$1"; }

# ── Argument parsing ───────────────────────────────────────────────────────
for arg in "$@"; do
    case "$arg" in
        --no-build) DO_BUILD=0 ;;
        --docker)   DOCKER_MODE=1 ;;
        -h|--help)
            sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) fail "unknown argument: $arg (try --help)" ;;
    esac
done

# ── Platform detection ─────────────────────────────────────────────────────
detect_platform() {
    OS="$(uname -s)"
    case "$OS" in
        Linux)  GOOS="linux" ;;
        Darwin) GOOS="darwin" ;;
        *)      fail "unsupported OS: $OS. On Windows use WSL2, or the Docker route (--docker)." ;;
    esac

    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64|amd64)   GOARCH="amd64" ;;
        arm64|aarch64)  GOARCH="arm64" ;;
        *)              fail "unsupported architecture: $ARCH" ;;
    esac

    log "platform: $GOOS/$GOARCH"
}

# Portable "is $1 >= $2" for dotted versions (no GNU sort -V on macOS).
version_ge() {
    awk -v have="$1" -v want="$2" 'BEGIN {
        h = split(have, H, "."); w = split(want, W, ".");
        for (i = 1; i <= 3; i++) {
            if ((H[i] + 0) > (W[i] + 0)) exit 0;
            if ((H[i] + 0) < (W[i] + 0)) exit 1;
        }
        exit 0;
    }'
}

# Read the Go version the project actually requires from go.mod.
required_go_version() {
    if [ -f go.mod ]; then
        awk '/^go /{print $2; exit}' go.mod
    else
        echo "$FALLBACK_GO_VERSION"
    fi
}

# ── Step 1: Go toolchain ───────────────────────────────────────────────────
ensure_go() {
    step "1/4  Go toolchain"

    local want
    want="$(required_go_version)"
    [ -n "$want" ] || want="$FALLBACK_GO_VERSION"

    if command -v go >/dev/null 2>&1; then
        local have
        have="$(go version | awk '{print $3}' | sed 's/^go//')"
        if version_ge "$have" "$want"; then
            log "Go $have found (project needs >= $want)"
            return 0
        fi
        warn "Go $have is too old — this project needs >= $want."
    else
        info "Go is not installed (project needs >= $want)."
    fi

    if [ "$GOOS" = "darwin" ] && command -v brew >/dev/null 2>&1; then
        info "Installing Go with Homebrew..."
        brew install go || fail "brew install go failed. Install Go manually from https://go.dev/dl/"
        log "Go installed: $(go version)"
        return 0
    fi

    info "Downloading Go $want for $GOOS/$GOARCH ..."
    local tarball="/tmp/go${want}.${GOOS}-${GOARCH}.tar.gz"
    local url="https://go.dev/dl/go${want}.${GOOS}-${GOARCH}.tar.gz"

    if ! curl -fsSL "$url" -o "$tarball"; then
        fail "could not download Go $want from $url
       Install Go >= $want manually (https://go.dev/dl/) and re-run this script."
    fi

    info "Extracting to /usr/local (needs sudo)..."
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "$tarball"
    rm -f "$tarball"

    export PATH="/usr/local/go/bin:$PATH"
    for rc in "$HOME/.profile" "$HOME/.zshrc"; do
        [ -f "$rc" ] || continue
        grep -q '/usr/local/go/bin' "$rc" 2>/dev/null || \
            echo 'export PATH="/usr/local/go/bin:$PATH"' >> "$rc"
    done

    command -v go >/dev/null 2>&1 || fail "Go installed but not on PATH. Open a new shell and re-run."
    log "Go installed: $(go version)"
}

# ── Step 2: Source checkout ────────────────────────────────────────────────
ensure_source() {
    step "2/4  Source code"

    # Already inside a shhgit checkout? (check the module path, not the name)
    if [ -f go.mod ] && grep -q "shhgot\|shhgit" go.mod 2>/dev/null; then
        INSTALL_DIR="$(pwd)"
        log "using existing checkout: $INSTALL_DIR"
        return 0
    fi

    command -v git >/dev/null 2>&1 || fail "git is required. Install git and re-run."

    if [ -d "$INSTALL_DIR/.git" ]; then
        info "Updating existing checkout at $INSTALL_DIR ..."
        git -C "$INSTALL_DIR" pull --ff-only || warn "could not update; using what is on disk"
    else
        info "Cloning $REPO_SLUG into $INSTALL_DIR ..."
        mkdir -p "$(dirname "$INSTALL_DIR")"
        git clone --depth 1 "$REPO_URL" "$INSTALL_DIR" || fail "git clone failed"
    fi

    cd "$INSTALL_DIR"
    log "checkout ready: $(pwd)"
}

# ── Step 3: Build ──────────────────────────────────────────────────────────
do_build() {
    step "3/4  Build"

    [ "$DO_BUILD" -eq 1 ] || { warn "skipped (--no-build)"; return 0; }

    export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
    info "Downloading Go modules..."
    go mod download || fail "go mod download failed (no network?)"

    info "Compiling..."
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BINARY" ./cmd/shhgit || fail "build failed"

    local size
    size="$(du -h "$BINARY" 2>/dev/null | cut -f1 || echo '?')"
    log "built ./$BINARY ($size)"
}

# ── Step 4: Config ─────────────────────────────────────────────────────────
configure() {
    step "4/4  Configuration"

    if [ -f config.yaml ]; then
        log "config.yaml already exists — leaving it alone"
    elif [ -f config.yaml.example ]; then
        cp config.yaml.example config.yaml
        chmod 600 config.yaml          # it will hold tokens
        log "created config.yaml from config.yaml.example"
        warn "add your GitHub token(s) to github_access_tokens before scanning GitHub"
    else
        warn "no config.yaml.example found — you will need to create config.yaml yourself"
    fi
}

# ── Smoke test ─────────────────────────────────────────────────────────────
smoke_test() {
    [ "$DO_BUILD" -eq 1 ] || return 0
    [ -x "./$BINARY" ] || { warn "binary not executable; skipping smoke test"; return 0; }

    if ./"$BINARY" -h 2>&1 | grep -qi 'usage'; then
        log "smoke test passed — the binary runs and lists its flags"
    else
        warn "could not verify the binary (it may still work)"
    fi
}

# ── Summary ────────────────────────────────────────────────────────────────
summary() {
    printf "\n${GREEN}${BOLD}Setup complete${NC}\n\n"
    printf "  Location:  %s\n" "$(pwd)"
    printf "  Binary:    %s/%s\n" "$(pwd)" "$BINARY"
    printf "  Config:    %s/config.yaml\n" "$(pwd)"
    printf "\n${BOLD}Next steps${NC}\n"
    printf "  1. Edit config.yaml and add your GitHub token(s) to github_access_tokens\n"
    printf "     (skip this if you only ever scan local code with --local)\n"
    printf "\n  2. Run it:\n"
    printf "       ./%s                 # terminal UI (default)\n" "$BINARY"
    printf "       ./%s --web           # web dashboard on http://127.0.0.1:8080\n" "$BINARY"
    printf "       ./%s --local ./code  # scan a local directory, no tokens needed\n" "$BINARY"
    printf "\n  Docs: https://github.com/%s#readme\n\n" "$REPO_SLUG"
}

docker_notice() {
    cat <<EOF

${BOLD}Docker route${NC}

  shhgit also runs from Docker. From a checkout of the repo:

      cp .env.example .env
      docker compose up -d
      # dashboard on http://localhost:8080

  The container needs a config.yaml; mount yours with
  -v "\$(pwd)/config.yaml:/app/config.yaml:ro" or set GITHUB_TOKEN in .env.

EOF
}

main() {
    printf "\n${BOLD}shhgit installer${NC} — ${REPO_SLUG}\n"

    if [ "$DOCKER_MODE" -eq 1 ]; then
        docker_notice
        exit 0
    fi

    detect_platform
    ensure_go
    ensure_source
    do_build
    configure
    smoke_test
    summary
}

main
