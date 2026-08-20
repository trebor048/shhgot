.PHONY: help build dev dev-web dev-server dev-cli docker-up docker-down docker-logs clean

help:
	@echo "shhgit development commands:"
	@echo ""
	@echo "  make build          - Build CLI and server binaries"
	@echo "  make dev            - Start all services for development"
	@echo "  make dev-web        - Start frontend dev server (requires backend running)"
	@echo "  make dev-server     - Start Go backend server"
	@echo "  make dev-cli        - Start CLI for local scanning"
	@echo "  make docker-up      - Start all services with Docker Compose"
	@echo "  make docker-down    - Stop Docker Compose services"
	@echo "  make docker-logs    - View Docker Compose logs"
	@echo "  make clean          - Remove build artifacts and caches"
	@echo ""

# Build targets
build: build-cli build-server build-web

build-cli:
	go build -o shhgit ./cmd/shhgit

build-server:
	cd server && go build -o shhgit-server main.go

build-web:
	cd frontend && npm install && npm run build

# Development targets
dev: dev-server dev-web

dev-server:
	cd server && go run main.go -port 8000

dev-web:
	cd frontend && npm install && npm run dev

dev-cli:
	go run ./cmd/shhgit --config-path . --live http://localhost:8000/api/push

# Docker targets
docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

docker-build:
	docker-compose build

# Cleanup
clean:
	rm -f shhgit shhgit-server
	cd frontend && rm -rf dist node_modules
	cd server && rm -f shhgit-server
	go clean
