# Focus Agent

A local AI-powered productivity agent that runs on macOS, integrating with Google Workspace to automate task management, email triage, and daily planning.

## Features

- **Gmail Integration**: Automatic email triage, thread summarization, and task extraction
- **Google Drive Sync**: Monitor document changes and link to meetings
- **Calendar Integration**: Event tracking and meeting preparation
- **Google Tasks Sync**: Unified task management across platforms
- **AI-Powered Insights**: Thread summaries, task extraction, and reply drafting using Gemini 1.5 Flash
- **Smart Prioritization**: Multi-factor task scoring based on impact, urgency, effort, and stakeholders
- **Daily Briefs**: Morning and midday planning delivered via Google Chat
- **Follow-up Tracking**: Automatic reminders for threads needing responses
- **Local-First**: All data stored locally in SQLite with intelligent caching

## Architecture

- **Language**: Go 1.21+
- **Database**: SQLite with FTS5 for full-text search
- **AI**: Google Gemini 1.5 Flash (free tier via AI Studio)
- **Deployment**: LaunchAgent on macOS for automatic startup
- **Privacy**: OAuth 2.0 with read-only scopes, local data storage

## Prerequisites

1. **Go 1.21+** installed
2. **Google Cloud Project** with the following APIs enabled:
   - Gmail API
   - Google Drive API
   - Google Calendar API
   - Google Tasks API
3. **OAuth 2.0 Credentials** from Google Cloud Console
4. **Gemini API Key** from [Google AI Studio](https://aistudio.google.com/app/apikey)
5. **Google Chat Webhook URL** for receiving briefs

## Quick Start

### 1. Clone and Build

```bash
git clone https://github.com/alexrabarts/focus-agent.git
cd focus-agent
make build
```

### 2. Configure

```bash
# Create config directory
mkdir -p ~/.focus-agent

# Copy and edit the example config
cp configs/config.example.yaml ~/.focus-agent/config.yaml

# Edit with your credentials
vim ~/.focus-agent/config.yaml
```

Required configuration:
- Google OAuth credentials (client_id, client_secret)
- Gemini API key
- Google Chat webhook URL

### 3. Authenticate

```bash
# Run OAuth flow to authenticate with Google
./bin/focus-agent -auth
```

This will open your browser for Google authentication. Grant the requested permissions.

### 4. Test Run

```bash
# Run once to test
./bin/focus-agent -once

# Generate and send a daily brief immediately
./bin/focus-agent -brief
```

### 5. Install as Service

```bash
# Install LaunchAgent for automatic startup
make install

# Start the service
launchctl load ~/Library/LaunchAgents/com.rabarts.focus-agent.plist

# Check status
launchctl list | grep rabarts
```

## Usage

### Command Line Options

```bash
focus-agent [options]

Options:
  -config string    Path to config file (default: ~/.focus-agent/config.yaml)
  -once            Run sync once and exit
  -auth            Run OAuth authentication only
  -brief           Generate and send brief immediately
  -version         Show version
```

### Daily Workflow

1. **Morning Brief (7:45 AM)**: Receive your daily plan with top tasks and meetings
2. **Continuous Sync**: Email, calendar, and task updates every 5-15 minutes
3. **Midday Re-plan (1:00 PM)**: Progress check and afternoon priorities
4. **Follow-ups**: Hourly checks for threads needing responses

### Task Scoring Formula

Tasks are scored using:
```
Score = 0.4*Impact + 0.35*Urgency + 0.15*Stakeholder - 0.1*Effort
```

- **Impact**: 1-5 scale of business value
- **Urgency**: 1-5 based on due date proximity
- **Stakeholder**: Internal (1.0), External (1.5), Executive (2.0)
- **Effort**: Small (0.5), Medium (1.0), Large (1.5)

## Development

### Project Structure

```
focus-agent/
├── cmd/agent/          # Main application entry point
├── internal/
│   ├── config/         # Configuration management
│   ├── db/            # Database layer and models
│   ├── google/        # Google API clients
│   ├── llm/           # Gemini AI integration
│   ├── planner/       # Task prioritization logic
│   └── scheduler/     # Job scheduling
├── migrations/        # Database schema
├── scripts/           # Setup and utility scripts
└── configs/           # Example configuration
```

### Building from Source

```bash
# Get dependencies
go mod download

# Run tests
go test ./...

# Build binary
go build -o bin/focus-agent cmd/agent/main.go

# Build with version info
make build VERSION=1.0.0
```

### Database Schema

The SQLite database includes:
- `messages`: Email storage with FTS5 indexing
- `threads`: Conversation tracking with summaries
- `tasks`: Unified task management
- `events`: Calendar events
- `docs`: Drive documents
- `llm_cache`: AI response caching
- `usage`: API usage tracking

## Configuration

### config.yaml Structure

```yaml
database:
  path: ~/.focus-agent/data.db

google:
  client_id: YOUR_CLIENT_ID
  client_secret: YOUR_CLIENT_SECRET
  polling_minutes:
    gmail: 5
    drive: 10
    calendar: 15
    tasks: 15

gemini:
  api_key: YOUR_GEMINI_KEY
  model: gemini-1.5-flash
  cache_hours: 24

chat:
  webhook_url: YOUR_WEBHOOK_URL

schedule:
  daily_brief_time: "07:45"
  replan_time: "13:00"
  timezone: America/Los_Angeles

planner:
  weights:
    impact: 0.4
    urgency: 0.35
    stakeholder: 0.15
    effort: 0.1
```

## Troubleshooting

### Check Logs

```bash
# View service logs
tail -f ~/.focus-agent/log/out.log
tail -f ~/.focus-agent/log/err.log

# Check LaunchAgent logs
log show --predicate 'process == "focus-agent"' --last 1h
```

### Reset Authentication

```bash
# Remove token and re-authenticate
rm ~/.focus-agent/token.json
./bin/focus-agent -auth
```

### Database Issues

```bash
# Backup database
cp ~/.focus-agent/data.db ~/.focus-agent/data.db.backup

# Reset database (WARNING: loses all data)
rm ~/.focus-agent/data.db
./bin/focus-agent -once
```

## Privacy & Security

- **Read-Only Access**: Uses minimal OAuth scopes (read-only by default)
- **Local Storage**: All data stored locally in SQLite
- **Token Security**: OAuth tokens stored with 0600 permissions
- **No Cloud Dependencies**: Runs entirely on your machine
- **Caching**: LLM responses cached locally to minimize API calls

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing`)
3. Commit changes (`git commit -am 'Add amazing feature'`)
4. Push to branch (`git push origin feature/amazing`)
5. Open a Pull Request

## License

MIT License - see LICENSE file for details

## Acknowledgments

- Google Workspace APIs for integration capabilities
- Gemini AI for intelligent processing
- SQLite for reliable local storage
- The Go community for excellent libraries