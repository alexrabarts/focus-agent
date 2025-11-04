# Deployment & Operations

This document contains operational runbooks and deployment procedures for Focus Agent.

## Server Management

### LaunchAgent Services (macOS)

**Restart services:**
```bash
# Restart focus-agent
launchctl unload ~/Library/LaunchAgents/com.rabarts.focus-agent.plist
launchctl load ~/Library/LaunchAgents/com.rabarts.focus-agent.plist

# Check status
launchctl list | grep rabarts

# View logs
tail -f ~/.focus-agent/log/*.log
```

**Full restart (both services):**
```bash
# Stop both
launchctl unload ~/Library/LaunchAgents/com.rabarts.focus-agent.plist
launchctl unload ~/Library/LaunchAgents/com.rabarts.focus-agent.ngrok.plist

# Start both
launchctl load ~/Library/LaunchAgents/com.rabarts.focus-agent.plist
launchctl load ~/Library/LaunchAgents/com.rabarts.focus-agent.ngrok.plist

# Verify
launchctl list | grep rabarts
curl http://localhost:8081/health
```

### Production Service (Linux/systemd)

**Location:** `/srv/focus-agent/`

**Restart service:**
```bash
sudo systemctl restart focus-agent
sudo systemctl status focus-agent
```

**View logs:**
```bash
sudo journalctl -u focus-agent -f
```

## Task Enrichment Backfill

The `-enrich-tasks` flag enriches existing email-extracted tasks with AI-generated descriptions. This adds context from thread messages to tasks that have missing or short (< 50 chars) descriptions.

### LLM Strategy (Priority Order)

1. **Ollama (Mistral 7B)** - Free, self-hosted on alex-mm:11434
2. Claude CLI (Haiku) - Free (via Claude.ai account)
3. Gemini 2.5 Flash - $0.20 per 1M tokens (paid fallback)

### Configuration

- Ollama URL: `http://alex-mm:11434`
- Ollama Model: `mistral:latest` (7B parameters)
- Rate limit: No limit (self-hosted)
- Caching: 24 hours via LLM cache to reduce costs
- Fallback: Automatic if Ollama is unreachable

### When to Run

- After major email imports
- When task descriptions are incomplete
- On demand to improve task context

### Procedure

```bash
# 1. Stop API server (DuckDB doesn't support concurrent writes)
sudo pkill -f "focus-agent.*-api"

# 2. Run enrichment
sudo -u alex /srv/focus-agent/focus-agent \
  -config /srv/focus-agent/config.yaml \
  -enrich-tasks

# 3. Restart API server
sudo -u alex /srv/focus-agent/focus-agent \
  -config /srv/focus-agent/config.yaml \
  -api > /tmp/focus-agent-api.log 2>&1 &
```

### What It Does

- Finds Gmail tasks with `status = 'pending'` and short/missing descriptions
- Processes up to 100 tasks at a time
- Shows cost estimate before processing
- Displays progress every 10 tasks
- Logs success/failure for each task

### Output Example

```
Finding email-extracted tasks that need enrichment...
Found 42 tasks to enrich
═══════════════════════════════════════════════════════
🤖 TASK ENRICHMENT ESTIMATE:
   Tasks to enrich: 42
   Estimated tokens: ~29400 tokens
   Estimated cost: ~$0.0059
═══════════════════════════════════════════════════════
Enriching task 1/42: Follow up on proposal
✓ Enriched: Discussion with client about Q1 proposal...
[...]
Progress: 10/42 tasks | Elapsed: 2m15s | Avg: 13s/task | Est. remaining: 7m
```

### Last Run

**Date:** October 26, 2025
**Result:** 0 tasks needed enrichment (all tasks already have sufficient descriptions)

## API Health Checks

**Check API health:**
```bash
curl http://localhost:8081/health
```

**Remote health check (via Tailscale):**
```bash
curl https://alex-het:8081/health
```
