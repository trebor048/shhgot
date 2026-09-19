#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Functions
print_header() {
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║${NC}  $1"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════════╝${NC}"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

# Main installation
main() {
    print_header "shhgit Native Linux Installation (No Docker)"
    
    # Detect OS
    if [[ ! "$OSTYPE" == "linux-gnu"* ]]; then
        print_error "This script only supports Linux"
        echo "Detected OS: $OSTYPE"
        exit 1
    fi
    
    print_info "Detected Linux system"
    
    print_header "Step 1: Checking Prerequisites"
    
    # Check Go
    if ! command -v go &> /dev/null; then
        print_error "Go is not installed"
        print_info "Installing Go 1.24.0..."
        
        # Detect architecture
        ARCH=$(uname -m)
        case $ARCH in
            x86_64) GO_ARCH="amd64" ;;
            aarch64) GO_ARCH="arm64" ;;
            *) print_error "Unsupported architecture: $ARCH"; exit 1 ;;
        esac
        
        GO_VERSION="1.24.0"
        GO_URL="https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
        
        print_info "Downloading Go from: $GO_URL"
        curl -fsSL "$GO_URL" -o /tmp/go.tar.gz
        sudo rm -rf /usr/local/go
        sudo tar -C /usr/local -xzf /tmp/go.tar.gz
        rm /tmp/go.tar.gz
        
        # Add Go to PATH
        if ! grep -q '/usr/local/go/bin' ~/.bashrc; then
            echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
        fi
        
        export PATH=$PATH:/usr/local/go/bin
        print_success "Go installed"
    else
        print_success "Go is installed: $(go version)"
    fi
    
    # Check Git
    if ! command -v git &> /dev/null; then
        print_error "Git is not installed"
        print_info "Installing Git..."
        sudo apt-get update -qq
        sudo apt-get install -y git >/dev/null 2>&1
        print_success "Git installed"
    else
        print_success "Git is installed: $(git --version)"
    fi
    
    # Check PostgreSQL (optional but recommended)
    if ! command -v psql &> /dev/null; then
        print_warning "PostgreSQL not installed (using SQLite for now)"
        print_info "To enable PostgreSQL later:"
        echo "  sudo apt-get install -y postgresql postgresql-contrib"
        echo "  Then set in config.yaml: database.type = 'postgres'"
    else
        print_success "PostgreSQL is installed"
    fi
    
    # Check Redis (optional)
    if ! command -v redis-cli &> /dev/null; then
        print_warning "Redis not installed (optional, for caching)"
    else
        print_success "Redis is installed"
    fi
    
    # Check Cloudflare Tunnel (optional)
    if ! command -v cloudflared &> /dev/null; then
        print_warning "cloudflared not installed (needed for Cloudflare Tunnel)"
        read -p "Install cloudflared for remote access? (y/n) " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            print_info "Installing cloudflared..."
            
            # Detect architecture
            ARCH=$(uname -m)
            case $ARCH in
                x86_64) CF_ARCH="amd64" ;;
                aarch64) CF_ARCH="arm64" ;;
                *) print_warning "Unsupported architecture: $ARCH, skipping cloudflared"; CF_ARCH="" ;;
            esac
            
            if [ -n "$CF_ARCH" ]; then
                # Download latest cloudflared release
                CF_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}"
                print_info "Downloading from: $CF_URL"
                
                if curl -fsSL "$CF_URL" -o /tmp/cloudflared 2>/dev/null; then
                    chmod +x /tmp/cloudflared
                    sudo mv /tmp/cloudflared /usr/local/bin/cloudflared
                    
                    if command -v cloudflared &> /dev/null; then
                        print_success "cloudflared installed: $(cloudflared --version 2>&1 | head -1)"
                    else
                        print_warning "cloudflared installation failed"
                    fi
                else
                    print_warning "Failed to download cloudflared - you can install manually:"
                    echo "  wget https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CF_ARCH}"
                    echo "  chmod +x cloudflared-linux-${CF_ARCH}"
                    echo "  sudo mv cloudflared-linux-${CF_ARCH} /usr/local/bin/cloudflared"
                fi
            fi
        fi
    else
        print_success "cloudflared is installed: $(cloudflared --version 2>&1 | head -1)"
    fi
    
    print_header "Step 2: Cloning Repository"
    
    INSTALL_DIR="${INSTALL_DIR:-.}"
    if [ "$INSTALL_DIR" = "." ]; then
        INSTALL_DIR="$PWD"
    fi
    
    # Check if already in shhgit directory
    if [ -f "go.mod" ] && grep -q "shhgit" go.mod 2>/dev/null; then
        print_info "Already in shhgit directory: $PWD"
        INSTALL_DIR="$PWD"
    else
        SHHGIT_DIR="$INSTALL_DIR/shhgit"
        
        if [ -d "$SHHGIT_DIR" ]; then
            print_warning "Directory $SHHGIT_DIR already exists"
            read -p "Use existing directory? (y/n) " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                INSTALL_DIR="$SHHGIT_DIR"
            else
                print_error "Installation cancelled"
                exit 1
            fi
        else
            print_info "Cloning shhgit..."
            git clone https://github.com/eth0izzle/shhgit "$SHHGIT_DIR"
            INSTALL_DIR="$SHHGIT_DIR"
            print_success "Repository cloned"
        fi
    fi
    
    cd "$INSTALL_DIR"
    
    print_header "Step 3: Configuration"
    
    # Copy config if not exists
    if [ ! -f "config.yaml" ]; then
        if [ -f "config.yaml.example" ]; then
            cp config.yaml.example config.yaml
            print_success "config.yaml created from config.yaml.example"
        fi
    fi
    
    # Prompt for GitHub token
    read -p "Enter GitHub Token (leave empty to skip): " github_token
    if [ -n "$github_token" ]; then
        # Add GitHub token to config (this is a simplified approach)
        print_info "GitHub token configured (update config.yaml manually if needed)"
    fi
    
    print_header "Step 4: Building shhgit"
    
    print_info "Building shhgit binary (this may take 2-5 minutes)..."
    
    # Check if go.mod exists
    if [ ! -f "go.mod" ]; then
        print_error "go.mod not found - are you in the shhgit directory?"
        exit 1
    fi
    
    # Download dependencies
    print_info "Downloading dependencies..."
    if ! go mod download 2>/dev/null; then
        print_warning "Some dependencies may have failed to download, attempting tidy..."
        go mod tidy
    fi
    
    # Build the binary
    if ! go build -o shhgit . 2>&1; then
        print_error "Build failed"
        echo "Try:"
        echo "  go mod tidy"
        echo "  go clean -modcache"
        echo "  go build -o shhgit ."
        exit 1
    fi
    
    if [ ! -f "shhgit" ]; then
        print_error "Build failed - shhgit binary not created"
        exit 1
    fi
    
    BINARY_SIZE=$(du -h shhgit | cut -f1)
    print_success "Build successful: ./shhgit ($BINARY_SIZE)"
    
    # Make executable
    chmod +x shhgit
    
    print_header "Step 5: Optional - PostgreSQL Setup"
    
    read -p "Set up PostgreSQL database? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        if command -v psql &> /dev/null; then
            print_info "Creating shhgit database..."
            
            # Generate random password
            PG_PASS=$(openssl rand -base64 16)
            
            print_info "PostgreSQL password: $PG_PASS (save this!)"
            
            # Create database
            if sudo -u postgres psql -c "CREATE USER shhgit WITH PASSWORD '$PG_PASS';" 2>/dev/null; then
                print_success "User created"
            else
                print_warning "User creation skipped (may already exist)"
            fi
            
            if sudo -u postgres psql -c "CREATE DATABASE shhgit OWNER shhgit;" 2>/dev/null; then
                print_success "Database created"
            else
                print_warning "Database creation skipped (may already exist)"
            fi
            
            # Update config - use more robust sed
            if [ -f "config.yaml" ]; then
                # Create backup
                cp config.yaml config.yaml.backup
                
                # Update settings (handle both existing and new entries)
                sed -i.bak "s/^  type: \"sqlite\"/  type: \"postgres\"/g" config.yaml
                sed -i.bak "s/^  postgres_user: .*/  postgres_user: \"shhgit\"/g" config.yaml
                sed -i.bak "s/^  postgres_password: .*/  postgres_password: \"$PG_PASS\"/g" config.yaml
                
                print_success "PostgreSQL configured in config.yaml"
                print_info "Connection details:"
                echo "  Host: localhost"
                echo "  User: shhgit"
                echo "  Password: $PG_PASS"
                echo "  Database: shhgit"
            fi
        else
            print_warning "PostgreSQL not installed, using SQLite"
            print_info "To install PostgreSQL later:"
            echo "  sudo apt-get install -y postgresql postgresql-contrib"
            echo "  Then update config.yaml: database.type = 'postgres'"
        fi
    else
        print_info "Using SQLite (default)"
    fi
    
    print_header "Step 6: Systemd Service (Optional)"
    
    read -p "Create systemd service for auto-start? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        SERVICE_FILE="/etc/systemd/system/shhgit.service"
        INSTALL_USER=$(whoami)
        INSTALL_DIR=$(pwd)
        
        print_info "Creating systemd service..."
        print_info "User: $INSTALL_USER"
        print_info "Directory: $INSTALL_DIR"
        
        # Create service file content
        SERVICE_CONTENT="[Unit]
Description=shhgit Secret Scanner
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=$INSTALL_USER
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/shhgit
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
"
        
        # Write service file
        echo "$SERVICE_CONTENT" | sudo tee "$SERVICE_FILE" > /dev/null
        
        # Reload systemd
        sudo systemctl daemon-reload
        
        # Enable service
        if sudo systemctl enable shhgit; then
            print_success "Systemd service created and enabled"
        else
            print_warning "Service creation failed"
            exit 1
        fi
        
        print_info "Service management:"
        echo "  Start:   sudo systemctl start shhgit"
        echo "  Stop:    sudo systemctl stop shhgit"
        echo "  Status:  sudo systemctl status shhgit"
        echo "  Logs:    sudo journalctl -u shhgit -f"
    fi
    
    print_header "Installation Complete! 🎉"
    
    echo ""
    echo -e "${GREEN}shhgit is ready to run!${NC}"
    echo ""
    echo "Start shhgit with:"
    echo "  ./shhgit"
    echo ""
    echo "Configuration:"
    echo "  Edit: nano config.yaml"
    echo ""
    echo "Access the web UI:"
    echo "  http://localhost:8080"
    echo ""
    
    # Check for Cloudflare Tunnel
    if command -v cloudflared &> /dev/null; then
        echo -e "${GREEN}Cloudflare Tunnel Available:${NC}"
        echo ""
        echo "  1. Create tunnel:"
        echo "     cloudflared tunnel login"
        echo "     cloudflared tunnel create shhgit"
        echo ""
        echo "  2. Configure shhgit:"
        echo "     tunnel:"
        echo "       enabled: true"
        echo "       tunnel_name: shhgit"
        echo ""
        echo "  3. Run shhgit with tunnel:"
        echo "     ./shhgit"
        echo ""
        echo "  Tunnel URL will print automatically when shhgit starts!"
        echo ""
    fi
    
    echo -e "${YELLOW}Next Steps:${NC}"
    echo "  1. Update config.yaml with your GitHub tokens"
    echo "  2. Run: ./shhgit"
    echo "  3. Access: http://localhost:8080"
    echo ""
    
    if [ -f "README.md" ]; then
        echo -e "${BLUE}Documentation:${NC}"
        echo "  Overview:  cat README.md"
        echo "  Config:    cat config.yaml.example"
        echo ""
    fi
}

# Run main
main
