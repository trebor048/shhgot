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

check_root() {
    if [ "$EUID" -eq 0 ]; then
        return 0
    else
        return 1
    fi
}

# Main installation
main() {
    print_header "shhgit Installation Script"
    
    # Detect OS
    if [[ ! "$OSTYPE" == "linux-gnu"* ]]; then
        print_error "This script only supports Linux"
        echo "Detected OS: $OSTYPE"
        exit 1
    fi
    
    print_info "Detected Linux system"
    
    # Check if running as root or with sudo
    if ! check_root; then
        print_warning "This script needs sudo privileges"
        print_info "Re-running with sudo..."
        sudo bash "$0"
        exit $?
    fi
    
    print_header "Step 1: Checking Prerequisites"
    
    # Check Docker
    if ! command -v docker &> /dev/null; then
        print_error "Docker is not installed"
        print_info "Installing Docker..."
        curl -fsSL https://get.docker.com -o get-docker.sh
        sudo sh get-docker.sh
        sudo usermod -aG docker "$SUDO_USER"
        print_success "Docker installed"
    else
        print_success "Docker is installed: $(docker --version)"
    fi
    
    # Check Docker Compose
    if ! command -v docker-compose &> /dev/null; then
        print_error "Docker Compose is not installed"
        print_info "Installing Docker Compose..."
        sudo curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
        sudo chmod +x /usr/local/bin/docker-compose
        print_success "Docker Compose installed"
    else
        print_success "Docker Compose is installed: $(docker-compose --version)"
    fi
    
    # Check Git
    if ! command -v git &> /dev/null; then
        print_error "Git is not installed"
        print_info "Installing Git..."
        apt-get update -qq
        apt-get install -y git >/dev/null 2>&1
        print_success "Git installed"
    else
        print_success "Git is installed: $(git --version)"
    fi
    
    print_header "Step 2: Cloning Repository"
    
    # Determine installation directory
    INSTALL_DIR="${INSTALL_DIR:-.}"
    if [ "$INSTALL_DIR" = "." ]; then
        INSTALL_DIR="$PWD"
    fi
    
    # Check if already in shhgit directory
    if [ -f "go.mod" ] && grep -q "shhgit" go.mod 2>/dev/null; then
        print_info "Already in shhgit directory: $PWD"
        INSTALL_DIR="$PWD"
    else
        print_info "Installation directory: $INSTALL_DIR"
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
    
    # Copy .env if not exists
    if [ ! -f ".env" ]; then
        print_info "Creating .env file..."
        if [ -f ".env.example" ]; then
            cp .env.example .env
            print_success ".env created from .env.example"
        else
            print_warning "No .env.example found, creating basic .env..."
            cat > .env << 'EOF'
SHHGIT_PORT=8080
SHHGIT_HOST=0.0.0.0
JWT_SECRET=change-me-to-random-string
ENABLE_AUTH=false
DB_TYPE=postgres
DB_NAME=shhgit
DB_USER=shhgit
DB_PASSWORD=change-me-to-secure-password
GITHUB_TOKEN=your_github_token_here
ENABLE_TUNNEL=false
TUNNEL_NAME=shhgit
PROMETHEUS_PORT=9090
GRAFANA_PORT=3000
GRAFANA_PASSWORD=change-me-to-secure-password
EOF
            print_success ".env created"
        fi
        
        # Prompt for essential configuration
        read -p "Enter GitHub Token (leave empty to skip): " github_token
        if [ -n "$github_token" ]; then
            sed -i "s|your_github_token_here|$github_token|g" .env
            print_success "GitHub token configured"
        fi
    else
        print_info ".env already exists"
    fi
    
    print_header "Step 4: Building Docker Image"
    
    # Build Docker image
    if [ -f "Dockerfile.new" ]; then
        print_info "Building shhgit Docker image (this may take 5-10 minutes)..."
        docker build -t shhgit:latest -f Dockerfile.new .
        print_success "Docker image built"
    elif [ -f "Dockerfile" ]; then
        print_info "Building shhgit Docker image (this may take 5-10 minutes)..."
        docker build -t shhgit:latest .
        print_success "Docker image built"
    else
        print_error "No Dockerfile found"
        exit 1
    fi
    
    print_header "Step 5: Starting Services"
    
    # Ask about Cloudflare Tunnel
    read -p "Enable Cloudflare Tunnel for remote access? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        print_info "Cloudflare Tunnel will be enabled"
        
        # Check if cloudflared is installed
        if ! command -v cloudflared &> /dev/null; then
            print_warning "cloudflared CLI not found, installing..."
            curl -L https://pkg.cloudflare.com/cloudflare-release-key.gpg | apt-key add -
            echo 'deb http://pkg.cloudflare.com/deb focal main' | tee /etc/apt/sources.list.d/cloudflare-main.list
            apt-get update -qq
            apt-get install -y cloudflared >/dev/null 2>&1
            print_success "cloudflared installed"
        fi
        
        sed -i 's|ENABLE_TUNNEL=false|ENABLE_TUNNEL=true|g' .env
        docker-compose --profile tunnel up -d
    else
        docker-compose up -d
    fi
    
    print_success "Services started"
    
    print_header "Step 6: Verification"
    
    # Wait for services to start
    print_info "Waiting for services to start (30 seconds)..."
    sleep 30
    
    # Check health
    if curl -s http://localhost:8080/health >/dev/null 2>&1; then
        print_success "shhgit is healthy"
    else
        print_warning "shhgit is not yet responding, it may still be starting"
    fi
    
    print_header "Installation Complete! 🎉"
    
    # Print access information
    echo ""
    echo -e "${GREEN}Access your shhgit instance:${NC}"
    echo ""
    echo -e "  ${BLUE}Web UI:${NC}         http://localhost:8080"
    echo -e "  ${BLUE}Prometheus:${NC}     http://localhost:9090"
    echo -e "  ${BLUE}Grafana:${NC}        http://localhost:3000"
    echo ""
    echo -e "${GREEN}Health Check:${NC}"
    echo "  curl http://localhost:8080/health"
    echo ""
    echo -e "${GREEN}View Logs:${NC}"
    echo "  docker-compose logs -f shhgit"
    echo ""
    
    # Check if Cloudflare Tunnel is enabled
    if grep -q "ENABLE_TUNNEL=true" .env 2>/dev/null; then
        echo -e "${GREEN}Cloudflare Tunnel:${NC}"
        echo "  Your tunnel will be accessible at:"
        echo "  https://<tunnel-name>.cfargotunnel.com"
        echo ""
        echo "  Configure your DNS CNAME record in Cloudflare dashboard:"
        echo "  Name: <tunnel-name>"
        echo "  Type: CNAME"
        echo "  Content: <tunnel-name>.cfargotunnel.com"
        echo ""
    fi
    
    echo -e "${GREEN}Configuration:${NC}"
    echo "  Edit .env file to customize settings:"
    echo "  nano .env"
    echo ""
    echo -e "${YELLOW}Next Steps:${NC}"
    echo "  1. Configure your GitHub token in .env if not done"
    echo "  2. Access the web UI at http://localhost:8080"
    echo "  3. Set up Cloudflare Tunnel for remote access (optional)"
    echo "  4. Create API tokens for authentication"
    echo ""
    echo -e "${BLUE}Documentation:${NC}"
    echo "  Overview:        ./README.md"
    echo "  Config example:  ./config.yaml.example"
    echo "  Env example:     ./.env.example"
    echo ""
}

# Run main
main
