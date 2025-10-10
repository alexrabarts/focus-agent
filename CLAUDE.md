# Claude Development Notes

## Project Conventions

**Package Naming:**
- All reverse domain notation uses `com.rabarts.*` (not `com.alexrabarts.*`)
- LaunchAgent plists: `com.rabarts.focus-agent`, `com.rabarts.focus-agent.ngrok`
- When creating new services or packages, use `com.rabarts` as the prefix

**Code Organization:**
- Go module: `github.com/alexrabarts/focus-agent`
- Binary: `focus-agent`
- Config directory: `~/.focus-agent`

## Recent Changes

### Single Thread Processing (2025-01-XX)
Changed 'p' key to process only the selected thread, not the entire queue:
- Added `ProcessSingleThread(threadID)` method to scheduler
- Pressing 'p' in queue list processes the currently selected/highlighted thread
- Pressing 'p' in queue detail view processes that specific thread
- Processing completes immediately and returns to queue list (local mode)
- Help text updated to clarify: "p: process selected" and "p: process this thread"
- After processing, the queue refreshes and processed item disappears
- Works seamlessly in both local mode (direct scheduler) and remote mode (API)

### Thread & Queue Detail Views (2025-01-XX)
Added ability to view full details in Threads and Queue tabs:

**Threads tab:**
- Press `Enter` on a thread to open detail view
- Shows full AI summary (not truncated)
- Displays all messages in the thread with timestamps
- Scrollable with ↑/↓ for long threads
- Press `Esc` or `q` to return to thread list

**Queue tab:**
- Press `Enter` on a queue item to view details
- Shows all messages in the thread
- Displays warning that thread hasn't been processed yet
- Press `p` to trigger processing (works from both list and detail view)
- Visual feedback: Shows "🔄 Processing queue with AI..." when processing is active
- Processing happens in the background; queue updates automatically via auto-refresh
- Works in both local mode (direct database access) and remote mode (via API)
- Processing indicator stays visible for 30 seconds (remote mode) or until completion (local mode)
- Scrollable with ↑/↓ for long threads
- Press `Esc` or `q` to return to queue list

Both views:
- Messages show snippet or truncated body (200 chars max)
- Scroll indicator shows current position

### Footer Refresh Indicator (2025-01-XX)
Replaced static "↻ 30s" refresh indicator with dynamic "Updated: Xm ago" timestamp:
- Shows relative time: "just now", "30s ago", "2m ago", etc.
- Updates naturally with UI interactions and auto-refresh
- Less distracting than static indicator
- Provides useful data freshness feedback

## Queue View Feature

Added a new "Queue" view to the TUI that shows threads waiting for AI processing.

### What Was Added

1. **New Queue View** (`internal/tui/queue.go`)
   - Shows threads without AI summaries
   - Displays estimated token cost
   - Shows processing status
   - Manual processing trigger with 'p' key

2. **API Endpoints**
   - `GET /api/queue` - List threads waiting for processing
   - `POST /api/queue/process` - Trigger AI processing manually

3. **TUI Integration**
   - Added "Queue" tab between "Priorities" and "Threads"
   - Auto-refresh with configurable interval
   - Remote API support for distributed setup

### Usage

**In TUI:**
- Navigate to Queue tab using `←/→` or `h/l`
- Press `p` to manually trigger AI processing
- Press `r` to refresh queue
- Queue count shows in footer when items waiting

**Via API:**
```bash
# Get queue
curl -H "Authorization: Bearer YOUR_AUTH_KEY" \
  https://your-server/api/queue

# Trigger processing
curl -X POST -H "Authorization: Bearer YOUR_AUTH_KEY" \
  https://your-server/api/queue/process
```

### Server Management

**Restart LaunchAgent services:**
```bash
# Restart focus-agent
launchctl unload ~/Library/LaunchAgents/com.rabarts.focus-agent.plist
launchctl load ~/Library/LaunchAgents/com.rabarts.focus-agent.plist

# Check status
launchctl list | grep rabarts

# View logs
tail -f ~/.focus-agent/log/*.log
```

**Check API health:**
```bash
curl http://localhost:8081/health
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
curl https://noncondescendingly-anteroparietal-tyesha.ngrok-free.dev/health
```

### Architecture Notes

**Scheduler Integration:**
- Server has `SetScheduler()` method to inject scheduler reference
- Allows API to trigger `ProcessNewMessages()` remotely
- Avoids circular dependency with interface

**Database Query:**
```sql
SELECT DISTINCT t.id, m.subject, m.from_addr, m.ts
FROM threads t
JOIN messages m ON t.id = m.thread_id
WHERE t.summary IS NULL OR t.summary = ''
GROUP BY t.id
ORDER BY m.ts DESC
LIMIT 100
```

**Token Estimation:**
- Conservative estimate: 500 tokens per thread
- Cost: $0.20 per 1M tokens (Gemini Flash free tier)
- Shows before processing starts

### Future Enhancements

- [ ] Progress bar during processing
- [ ] Real-time processing updates via WebSocket
- [ ] Selective processing (checkbox selection)
- [ ] Priority queue ordering
- [ ] Retry failed threads
- [ ] Processing history view
