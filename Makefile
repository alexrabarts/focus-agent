# Focus Agent Makefile

# Variables
BINARY_NAME=focus-agent
BINARY_PATH=bin/$(BINARY_NAME)
MAIN_PATH=cmd/agent/main.go
LAUNCHAGENT_PLIST=com.rabarts.focus-agent.plist
LAUNCHAGENT_PATH=~/Library/LaunchAgents/$(LAUNCHAGENT_PLIST)
SYSTEMD_UNIT_DIR=$(HOME)/.config/systemd/user
SYSTEMD_UNIT=focus-agent.service
UNAME_S:=$(shell uname -s)

# OS-specific paths
ifeq ($(UNAME_S),Darwin)
INSTALL_PATH=/usr/local/bin/$(BINARY_NAME)
CONFIG_DIR=~/.focus-agent
DEFAULT_INSTALL_DIR=$(CONFIG_DIR)
else
DEFAULT_INSTALL_DIR=/srv/focus-agent
INSTALL_PATH=$(DEFAULT_INSTALL_DIR)/$(BINARY_NAME)
CONFIG_DIR=$(DEFAULT_INSTALL_DIR)
endif

INSTALL_DIR?=$(DEFAULT_INSTALL_DIR)

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
install: build install-config ## Install the agent (binary, config, service)
	@echo "Installing $(BINARY_NAME)..."
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."; \
		sudo cp $(BINARY_PATH) $(INSTALL_PATH); \
		sudo chmod 755 $(INSTALL_PATH); \
		$(MAKE) install-launchagent; \
		echo ""; \
		echo "Installation complete!"; \
		echo ""; \
		echo "Next steps:"; \
		echo "1. Edit config: vim $(CONFIG_DIR)/config.yaml"; \
		echo "2. Authenticate: $(BINARY_NAME) -auth"; \
		echo "3. Start service: launchctl load $(LAUNCHAGENT_PATH)"; \
	elif [ "$$os" = "Linux" ]; then \
		mkdir -p $(INSTALL_DIR); \
		cp $(BINARY_PATH) $(INSTALL_DIR)/; \
		chmod 755 $(INSTALL_DIR)/$(BINARY_NAME); \
		mkdir -p $(SYSTEMD_UNIT_DIR); \
		sed "s|@INSTALL_DIR@|$(INSTALL_DIR)|g" systemd/$(SYSTEMD_UNIT) > $(SYSTEMD_UNIT_DIR)/$(SYSTEMD_UNIT); \
		systemctl --user daemon-reload >/dev/null 2>&1 || true; \
		echo "Installed systemd unit to $(SYSTEMD_UNIT_DIR)/$(SYSTEMD_UNIT)"; \
		echo ""; \
		echo "Installation complete!"; \
		echo ""; \
		echo "Next steps:"; \
		echo "1. Edit config: vim $(INSTALL_DIR)/config.yaml"; \
		echo "2. Authenticate: $(INSTALL_DIR)/$(BINARY_NAME) -config $(INSTALL_DIR)/config.yaml -auth"; \
		echo "3. Start service: systemctl --user enable --now $(SYSTEMD_UNIT)"; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

.PHONY: install-config
install-config: ## Install config files
	@echo "Setting up configuration..."
	@mkdir -p $(INSTALL_DIR)
	@mkdir -p $(INSTALL_DIR)/log
	@if [ ! -f $(INSTALL_DIR)/config.yaml ]; then \
		cp configs/config.example.yaml $(INSTALL_DIR)/config.yaml; \
		echo "Created config file at $(INSTALL_DIR)/config.yaml"; \
		echo "Please edit it with your credentials"; \
	else \
		echo "Config file already exists at $(INSTALL_DIR)/config.yaml"; \
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
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		if [ -f $(LAUNCHAGENT_PATH) ]; then \
			launchctl unload $(LAUNCHAGENT_PATH) 2>/dev/null || true; \
			rm -f $(LAUNCHAGENT_PATH); \
			echo "LaunchAgent removed"; \
		fi; \
		sudo rm -f $(INSTALL_PATH); \
		echo "Binary removed from $(INSTALL_PATH)"; \
		echo ""; \
		echo "Config and data preserved at $(CONFIG_DIR)"; \
		echo "To remove completely: rm -rf $(CONFIG_DIR)"; \
	elif [ "$$os" = "Linux" ]; then \
		systemctl --user disable --now $(SYSTEMD_UNIT) >/dev/null 2>&1 || true; \
		rm -f $(SYSTEMD_UNIT_DIR)/$(SYSTEMD_UNIT); \
		systemctl --user daemon-reload >/dev/null 2>&1 || true; \
		echo "Systemd unit removed"; \
		echo ""; \
		echo "Data preserved at $(INSTALL_DIR)"; \
		echo "To remove completely: rm -rf $(INSTALL_DIR)"; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

.PHONY: start
start: ## Start the service
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		echo "Starting LaunchAgent..."; \
		launchctl load $(LAUNCHAGENT_PATH); \
		echo "Service started. View logs with: tail -f $(CONFIG_DIR)/log/*.log"; \
	elif [ "$$os" = "Linux" ]; then \
		echo "Starting systemd user service..."; \
		systemctl --user daemon-reload; \
		systemctl --user enable --now $(SYSTEMD_UNIT); \
		echo "Service started. View logs with: journalctl --user-unit $(SYSTEMD_UNIT) -f"; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

.PHONY: stop
stop: ## Stop the service
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		echo "Stopping LaunchAgent..."; \
		launchctl unload $(LAUNCHAGENT_PATH) || true; \
		echo "Service stopped"; \
	elif [ "$$os" = "Linux" ]; then \
		echo "Stopping systemd user service..."; \
		systemctl --user disable --now $(SYSTEMD_UNIT) >/dev/null 2>&1 || true; \
		echo "Service stopped"; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

.PHONY: restart
restart: stop start ## Restart the service

.PHONY: deploy
deploy: build ## Deploy (build, copy binary, and restart service)
	@echo "Deploying $(BINARY_NAME)..."
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		sudo cp $(BINARY_PATH) $(INSTALL_PATH); \
		sudo chmod 755 $(INSTALL_PATH); \
		echo "Binary copied to $(INSTALL_PATH)"; \
		echo "Restarting service..."; \
		launchctl kickstart -k gui/$$(id -u)/com.rabarts.focus-agent; \
	elif [ "$$os" = "Linux" ]; then \
		cp $(BINARY_PATH) $(INSTALL_DIR)/; \
		chmod 755 $(INSTALL_DIR)/$(BINARY_NAME); \
		echo "Binary copied to $(INSTALL_DIR)"; \
		echo "Restarting service..."; \
		systemctl --user daemon-reload; \
		systemctl --user restart $(SYSTEMD_UNIT); \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi
	@echo "Deploy complete. View logs with: make logs"

.PHONY: status
status: ## Check service status
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		launchctl list | grep $(BINARY_NAME) || echo "Service not running"; \
	elif [ "$$os" = "Linux" ]; then \
		systemctl --user status $(SYSTEMD_UNIT) --no-pager || true; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

.PHONY: logs
logs: ## Tail the logs
	@os="$(UNAME_S)"; \
	if [ "$$os" = "Darwin" ]; then \
		echo "Showing logs (Ctrl+C to stop)..."; \
		tail -f $(CONFIG_DIR)/log/out.log $(CONFIG_DIR)/log/err.log; \
	elif [ "$$os" = "Linux" ]; then \
		journalctl --user-unit $(SYSTEMD_UNIT) -f; \
	else \
		echo "Unsupported OS $$os"; \
		exit 1; \
	fi

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
		rm -f $(INSTALL_DIR)/data.duckdb* $(INSTALL_DIR)/data.db*; \
		echo "Database reset"; \
	else \
		echo "Cancelled"; \
	fi

.PHONY: backup
backup: ## Backup config and database
	@echo "Creating backup..."
	@mkdir -p $(INSTALL_DIR)/backups
	@tar czf $(INSTALL_DIR)/backups/backup-$(shell date +%Y%m%d-%H%M%S).tar.gz \
		-C $(INSTALL_DIR) config.yaml data.duckdb token.json 2>/dev/null || true
	@echo "Backup created in $(INSTALL_DIR)/backups/"

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
	@duckdb $(INSTALL_DIR)/data.duckdb

.PHONY: db-stats
db-stats: build ## Show database statistics
	@echo "Database statistics:"
	@$(BINARY_PATH) -config $(INSTALL_DIR)/config.yaml -once 2>&1 | grep -A 5 "SYNC SUMMARY" || \
		echo "Unable to fetch stats. Ensure database is initialized."

# CI/CD targets
.PHONY: ci
ci: deps lint vet test build ## Run CI checks

.PHONY: check
check: fmt vet lint test ## Run all checks