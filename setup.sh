#!/usr/bin/env bash
#
# shhgit — One-shot Linux setup & build script
# ==============================================
# This script installs Go (if needed), sets up the project,
# builds the shhgit binary, and prepares config.yaml.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/trebor048/shhgot/main/setup.sh | bash
#   or
#   chmod +x setup.sh && ./setup.sh
#
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color
BOLD='\033[1m'

MIN_GO_VERSION="1.24.0"
REPO_URL="https://github.com/trebor048/shhgot.git"
INSTALL_DIR="${INSTALL_DIR:-$HOME/shhgit}"

# ─── Helpers ────────────────────────────────────────────────────────────
log()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
fail() { echo -e "${RED}[✗]${NC} $1"; exit 1; }

version_ge() {
    # Returns 0 if $1 >= $2 (semver comparison)
    printf '%s\n%s\n' "$2" "$1" | sort -V -C
}

# ─── Step 1: Install Go ─────────────────────────────────────────────────
install_go() {
    if command -v go &>/dev/null; then
        GO_VER=$(go version | grep -oP 'go\d+\.\d+\.\d+' | head -1)
        if version_ge "$GO_VER" "$MIN_GO_VERSION"; then
            log "Go $GO_VER already installed (>= $MIN_GO_VERSION)"
            return 0
        fi
        warn "Found Go $GO_VER but need >= $MIN_GO_VERSION. Installing latest..."
    fi

    log "Installing Go $MIN_GO_VERSION+ ..."

    local ARCH
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        armv7l)  ARCH="armv6l" ;;
        *)       fail "Unsupported architecture: $ARCH" ;;
    esac

    local GO_TAR="go${MIN_GO_VERSION}.linux-${ARCH}.tar.gz"
    local GO_URL="https://go.dev/dl/${GO_TAR}"

    log "Downloading $GO_URL ..."
    curl -fsSL "$GO_URL" -o "/tmp/${GO_TAR}" || fail "Failed to download Go"

    log "Extracting Go to /usr/local ..."
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "/tmp/${GO_TAR}"
    rm -f "/tmp/${GO_TAR}"

    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> "$HOME/.profile"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> "$HOME/.bashrc"

    log "Go $(go version) installed successfully"
}

# ─── Step 2: Clone repo ───────────────────────────────────────────────────
clone_repo() {
    if [ -d "$INSTALL_DIR/.git" ]; then
        log "Repo already exists at $INSTALL_DIR — pulling latest..."
        git -C "$INSTALL_DIR" pull --ff-only origin main 2>/dev/null || \
            warn "Could not pull. Using existing checkout."
    else
        log "Cloning $REPO_URL → $INSTALL_DIR ..."
        git clone "$REPO_URL" "$INSTALL_DIR" || fail "Failed to clone repo"
    fi
    cd "$INSTALL_DIR"
}

# ─── Step 3: Build ────────────────────────────────────────────────────────
build() {
    log "Downloading Go dependencies..."
    go mod download

    log "Building shhgit binary..."
    CGO_ENABLED=0 go build -ldflags="-s -w" -o shhgit . || fail "Build failed"

    log "Binary built: $(pwd)/shhgit ($(file shhgit | cut -d: -f2-))"
}

# ─── Step 4: Configure ────────────────────────────────────────────────────
configure() {
    if [ -f config.yaml ]; then
        log "config.yaml already exists — skipping"
    elif [ -f config.yaml.example ]; then
        log "Creating config.yaml from config.yaml.example ..."
        cp config.yaml.example config.yaml
        warn "⚠  Edit config.yaml to add your GitHub tokens before running!"
    else
        warn "⚠  No config.yaml or config.yaml.example found. You will need to create one."
    fi
}

# ─── Step 5: Verify ───────────────────────────────────────────────────────
verify() {
    log "Running quick smoke test..."
    if ./shhgit --version 2>/dev/null; then
        log "Smoke test passed!"
    else
        warn "shhgit binary exists but --version flag may not be supported"
        log "Try running: ./shhgit"
        log "Or with config:  ./shhgit --config-path config.yaml"
    fi
}

# ─── Main ─────────────────────────────────────────────────────────────────
main() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║   shhgit — Linux Setup & Build       ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════╝${NC}"
    echo ""

    install_go
    clone_repo
    # Ensure Go is in PATH for this session
    export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"
    build
    configure
    verify

    echo ""
    echo -e "${GREEN}${BOLD}═══ Setup complete! ═══${NC}"
    echo ""
    echo -e "  Binary:  ${BOLD}$(pwd)/shhgit${NC}"
    echo -e "  Config:  ${BOLD}$(pwd)/config.yaml${NC}"
    echo ""
    echo -e "  To use globally, add to your PATH:"
    echo -e "    ${BOLD}export PATH=\"$(pwd):\$PATH\"${NC}"
    echo ""
    echo -e "  Next steps:"
    echo -e "    1. Edit ${BOLD}config.yaml${NC} and add your GitHub tokens"
    echo -e "    2. Run: ${BOLD}./shhgit${NC}"
    echo ""
}

main "$@"
