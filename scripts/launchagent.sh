#!/bin/bash

# Focus Agent LaunchAgent Setup Script

set -e

AGENT_NAME="focus-agent"
PLIST_NAME="com.alexrabarts.focus-agent"
BINARY_PATH="/usr/local/bin/focus-agent"
CONFIG_PATH="$HOME/.focus-agent/config.yaml"
LOG_DIR="$HOME/.focus-agent/log"
LAUNCHAGENTS_DIR="$HOME/Library/LaunchAgents"
PLIST_FILE="$LAUNCHAGENTS_DIR/${PLIST_NAME}.plist"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Functions
print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

create_plist() {
    cat > "$PLIST_FILE" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${PLIST_NAME}</string>

    <key>ProgramArguments</key>
    <array>
        <string>${BINARY_PATH}</string>
        <string>-config</string>
        <string>${CONFIG_PATH}</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
        <key>Crashed</key>
        <true/>
    </dict>

    <key>StartInterval</key>
    <integer>60</integer>

    <key>StandardOutPath</key>
    <string>${LOG_DIR}/out.log</string>

    <key>StandardErrorPath</key>
    <string>${LOG_DIR}/err.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>

    <key>WorkingDirectory</key>
    <string>${HOME}</string>

    <key>ThrottleInterval</key>
    <integer>30</integer>

    <key>Nice</key>
    <integer>10</integer>
</dict>
</plist>
EOF
}

install() {
    echo "Installing Focus Agent LaunchAgent..."

    # Check if binary exists
    if [ ! -f "$BINARY_PATH" ]; then
        print_error "Binary not found at $BINARY_PATH"
        print_warning "Please run 'make install' first"
        exit 1
    fi

    # Check if config exists
    if [ ! -f "$CONFIG_PATH" ]; then
        print_warning "Config not found at $CONFIG_PATH"
        print_warning "A default config will be created, but you'll need to edit it"
    fi

    # Create LaunchAgents directory if it doesn't exist
    mkdir -p "$LAUNCHAGENTS_DIR"
    print_success "LaunchAgents directory ready"

    # Create log directory
    mkdir -p "$LOG_DIR"
    print_success "Log directory created at $LOG_DIR"

    # Stop existing service if running
    if launchctl list | grep -q "$PLIST_NAME"; then
        print_warning "Stopping existing service..."
        launchctl unload "$PLIST_FILE" 2>/dev/null || true
    fi

    # Create plist file
    create_plist
    print_success "LaunchAgent plist created at $PLIST_FILE"

    # Load the service
    echo "Loading LaunchAgent..."
    if launchctl load "$PLIST_FILE" 2>/dev/null; then
        print_success "LaunchAgent loaded successfully"
    else
        print_error "Failed to load LaunchAgent"
        print_warning "Try: launchctl load $PLIST_FILE"
        exit 1
    fi

    # Verify service is running
    sleep 2
    if launchctl list | grep -q "$PLIST_NAME"; then
        print_success "Service is running"
        echo ""
        echo "Focus Agent installed and started!"
        echo ""
        echo "Commands:"
        echo "  Check status:  launchctl list | grep $AGENT_NAME"
        echo "  View logs:     tail -f $LOG_DIR/*.log"
        echo "  Stop service:  launchctl unload $PLIST_FILE"
        echo "  Start service: launchctl load $PLIST_FILE"
    else
        print_error "Service failed to start"
        echo "Check logs at: $LOG_DIR/err.log"
        exit 1
    fi
}

uninstall() {
    echo "Uninstalling Focus Agent LaunchAgent..."

    # Unload service if running
    if launchctl list | grep -q "$PLIST_NAME"; then
        print_warning "Stopping service..."
        if launchctl unload "$PLIST_FILE" 2>/dev/null; then
            print_success "Service stopped"
        else
            print_error "Failed to stop service"
        fi
    fi

    # Remove plist file
    if [ -f "$PLIST_FILE" ]; then
        rm "$PLIST_FILE"
        print_success "LaunchAgent plist removed"
    else
        print_warning "Plist file not found"
    fi

    echo ""
    echo "LaunchAgent uninstalled"
    echo "Note: Binary, config, and data files are preserved"
}

status() {
    echo "Focus Agent Status:"
    echo ""

    # Check if plist exists
    if [ -f "$PLIST_FILE" ]; then
        print_success "LaunchAgent plist exists"
    else
        print_error "LaunchAgent plist not found"
    fi

    # Check if service is loaded
    if launchctl list | grep -q "$PLIST_NAME"; then
        print_success "Service is loaded"

        # Get PID and status
        STATUS=$(launchctl list | grep "$PLIST_NAME")
        echo "  $STATUS"
    else
        print_error "Service is not loaded"
    fi

    # Check if binary exists
    if [ -f "$BINARY_PATH" ]; then
        print_success "Binary exists at $BINARY_PATH"
    else
        print_error "Binary not found at $BINARY_PATH"
    fi

    # Check if config exists
    if [ -f "$CONFIG_PATH" ]; then
        print_success "Config exists at $CONFIG_PATH"
    else
        print_error "Config not found at $CONFIG_PATH"
    fi

    # Check recent logs
    if [ -d "$LOG_DIR" ]; then
        echo ""
        echo "Recent logs:"
        if [ -f "$LOG_DIR/out.log" ]; then
            echo "  stdout: $(tail -n 1 $LOG_DIR/out.log)"
        fi
        if [ -f "$LOG_DIR/err.log" ] && [ -s "$LOG_DIR/err.log" ]; then
            echo "  stderr: $(tail -n 1 $LOG_DIR/err.log)"
        fi
    fi
}

restart() {
    echo "Restarting Focus Agent..."

    if launchctl list | grep -q "$PLIST_NAME"; then
        launchctl unload "$PLIST_FILE" 2>/dev/null || true
        print_success "Service stopped"
    fi

    sleep 1

    if launchctl load "$PLIST_FILE" 2>/dev/null; then
        print_success "Service started"
    else
        print_error "Failed to start service"
        exit 1
    fi
}

logs() {
    echo "Showing Focus Agent logs (Ctrl+C to stop)..."
    echo ""

    if [ ! -d "$LOG_DIR" ]; then
        print_error "Log directory not found"
        exit 1
    fi

    # Use tail with multiple files
    tail -f "$LOG_DIR"/*.log
}

# Main script
case "${1:-}" in
    install)
        install
        ;;
    uninstall)
        uninstall
        ;;
    status)
        status
        ;;
    restart)
        restart
        ;;
    logs)
        logs
        ;;
    *)
        echo "Focus Agent LaunchAgent Manager"
        echo ""
        echo "Usage: $0 {install|uninstall|status|restart|logs}"
        echo ""
        echo "Commands:"
        echo "  install    - Install and start the LaunchAgent"
        echo "  uninstall  - Stop and remove the LaunchAgent"
        echo "  status     - Show current status"
        echo "  restart    - Restart the service"
        echo "  logs       - Tail the log files"
        exit 1
        ;;
esac