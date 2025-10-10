# Focus Agent Makefile

# Variables
BINARY_NAME=focus-agent
BINARY_PATH=bin/$(BINARY_NAME)
MAIN_PATH=cmd/agent/main.go
INSTALL_PATH=/usr/local/bin/$(BINARY_NAME)
CONFIG_DIR=~/.focus-agent
LAUNCHAGENT_PLIST=com.rabarts.focus-agent.plist
LAUNCHAGENT_PATH=~/Library/LaunchAgents/$(LAUNCHAGENT_PLIST)

# Version info
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME = $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT = $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Build flags
LDFLAGS = -ldflags "-X main.VERSION=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"
# DuckDB requires CGO
export CGO_ENABLED=1

# Default target
.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help message
	@echo "Focus Agent - Local AI Productivity Assistant"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Download Go module dependencies
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy

.PHONY: build
build: deps ## Build the binary
	@echo "Building $(BINARY_NAME) $(VERSION)..."
	@mkdir -p bin
	@go build $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "Binary built at $(BINARY_PATH)"

.PHONY: run
run: build ## Build and run the agent
	@echo "Running $(BINARY_NAME)..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml

.PHONY: run-once
run-once: build ## Run sync once and exit
	@echo "Running single sync..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -once

.PHONY: process
process: build ## Process threads with AI (summarize and extract tasks)
	@echo "Processing threads with AI..."
	@echo "This will summarize email threads and extract tasks using Gemini"
	@echo "Estimated cost: ~\$$0.10 for 1000 threads"
	@echo ""
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -process

.PHONY: auth
auth: build ## Run OAuth authentication flow
	@echo "Starting OAuth authentication..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -auth

.PHONY: brief
brief: build ## Generate and send daily brief immediately
	@echo "Generating daily brief..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -brief

.PHONY: tui
tui: build ## Run interactive TUI (Terminal User Interface)
	@echo "Starting TUI..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -tui

.PHONY: api
api: build ## Run API server with scheduler (for remote TUI access)
	@echo "Starting API server..."
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -api

.PHONY: ngrok
ngrok: ## Run ngrok tunnel to expose API server
	@echo "Starting ngrok tunnel..."
	@ngrok start --config $(CONFIG_DIR)/ngrok.yaml --all

.PHONY: test
test: ## Run tests
	@echo "Running tests..."
	@go test -v -race -cover ./...

.PHONY: test-coverage
test-coverage: ## Run tests with coverage report
	@echo "Running tests with coverage..."
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

.PHONY: lint
lint: ## Run linter
	@echo "Running linter..."
	@if command -v golangci-lint &> /dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Install with: brew install golangci-lint"; \
		exit 1; \
	fi

.PHONY: fmt
fmt: ## Format code
	@echo "Formatting code..."
	@go fmt ./...
	@gofmt -s -w .

.PHONY: vet
vet: ## Run go vet
	@echo "Running go vet..."
	@go vet ./...

.PHONY: install
install: build install-config install-launchagent ## Install the agent (binary, config, LaunchAgent)
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@sudo cp $(BINARY_PATH) $(INSTALL_PATH)
	@sudo chmod 755 $(INSTALL_PATH)
	@echo "Installation complete!"
	@echo ""
	@echo "Next steps:"
	@echo "1. Edit config: vim $(CONFIG_DIR)/config.yaml"
	@echo "2. Authenticate: $(BINARY_NAME) -auth"
	@echo "3. Start service: launchctl load $(LAUNCHAGENT_PATH)"

.PHONY: install-config
install-config: ## Install config files
	@echo "Setting up configuration..."
	@mkdir -p $(CONFIG_DIR)
	@mkdir -p $(CONFIG_DIR)/log
	@if [ ! -f $(CONFIG_DIR)/config.yaml ]; then \
		cp configs/config.example.yaml $(CONFIG_DIR)/config.yaml; \
		echo "Created config file at $(CONFIG_DIR)/config.yaml"; \
		echo "Please edit it with your credentials"; \
	else \
		echo "Config file already exists at $(CONFIG_DIR)/config.yaml"; \
	fi

.PHONY: install-launchagent
install-launchagent: ## Install LaunchAgent plist
	@echo "Installing LaunchAgent..."
	@mkdir -p ~/Library/LaunchAgents
	@./scripts/launchagent.sh install
	@echo "LaunchAgent installed at $(LAUNCHAGENT_PATH)"

.PHONY: uninstall
uninstall: ## Uninstall the agent
	@echo "Uninstalling $(BINARY_NAME)..."
	@if [ -f $(LAUNCHAGENT_PATH) ]; then \
		launchctl unload $(LAUNCHAGENT_PATH) 2>/dev/null || true; \
		rm -f $(LAUNCHAGENT_PATH); \
		echo "LaunchAgent removed"; \
	fi
	@sudo rm -f $(INSTALL_PATH)
	@echo "Binary removed from $(INSTALL_PATH)"
	@echo ""
	@echo "Config and data preserved at $(CONFIG_DIR)"
	@echo "To remove completely: rm -rf $(CONFIG_DIR)"

.PHONY: start
start: ## Start the LaunchAgent service
	@echo "Starting service..."
	@launchctl load $(LAUNCHAGENT_PATH)
	@echo "Service started"

.PHONY: stop
stop: ## Stop the LaunchAgent service
	@echo "Stopping service..."
	@launchctl unload $(LAUNCHAGENT_PATH)
	@echo "Service stopped"

.PHONY: restart
restart: stop start ## Restart the LaunchAgent service

.PHONY: status
status: ## Check service status
	@echo "Service status:"
	@launchctl list | grep $(BINARY_NAME) || echo "Service not running"

.PHONY: logs
logs: ## Tail the logs
	@echo "Showing logs (Ctrl+C to stop)..."
	@tail -f $(CONFIG_DIR)/log/out.log $(CONFIG_DIR)/log/err.log

.PHONY: clean
clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf bin/
	@rm -f coverage.out coverage.html
	@echo "Clean complete"

.PHONY: reset-db
reset-db: ## Reset the database (WARNING: deletes all data)
	@echo "WARNING: This will delete all data!"
	@read -p "Are you sure? (y/N) " -n 1 -r; \
	echo ""; \
	if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
		rm -f $(CONFIG_DIR)/data.duckdb* $(CONFIG_DIR)/data.db*; \
		echo "Database reset"; \
	else \
		echo "Cancelled"; \
	fi

.PHONY: backup
backup: ## Backup config and database
	@echo "Creating backup..."
	@mkdir -p $(CONFIG_DIR)/backups
	@tar czf $(CONFIG_DIR)/backups/backup-$(shell date +%Y%m%d-%H%M%S).tar.gz \
		-C $(CONFIG_DIR) config.yaml data.duckdb token.json 2>/dev/null || true
	@echo "Backup created in $(CONFIG_DIR)/backups/"

.PHONY: docker-build
docker-build: ## Build Docker image (future enhancement)
	@echo "Docker support coming soon..."
	# docker build -t focus-agent:$(VERSION) .

.PHONY: release
release: clean test build ## Create release build
	@echo "Creating release $(VERSION)..."
	@mkdir -p releases
	@tar czf releases/focus-agent-$(VERSION)-darwin-amd64.tar.gz \
		-C bin $(BINARY_NAME) \
		-C .. README.md LICENSE configs/config.example.yaml
	@echo "Release created at releases/focus-agent-$(VERSION)-darwin-amd64.tar.gz"

# Development database commands
.PHONY: db-shell
db-shell: ## Open DuckDB shell
	@echo "Opening DuckDB shell (use .help for commands, .quit to exit)..."
	@duckdb $(CONFIG_DIR)/data.duckdb

.PHONY: db-stats
db-stats: build ## Show database statistics
	@echo "Database statistics:"
	@$(BINARY_PATH) -config $(CONFIG_DIR)/config.yaml -once 2>&1 | grep -A 5 "SYNC SUMMARY" || \
		echo "Unable to fetch stats. Ensure database is initialized."

# CI/CD targets
.PHONY: ci
ci: deps lint vet test build ## Run CI checks

.PHONY: check
check: fmt vet lint test ## Run all checks