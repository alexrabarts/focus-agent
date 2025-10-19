# Focus Agent - Future Features

## Planned Features

### Interactive Chat Commands
**Priority:** High
**Status:** On Hold (waiting for domain migration)

Add interactive task management directly in Google Chat, allowing users to complete tasks, add new tasks, and view their task list without switching to the TUI.

**Requirements:**
- Custom domain with HTTPS (replacing ngrok)
- Google Chat app configured to send interaction events to webhook
- Focus Agent service accessible via public URL

**Commands:**
| Command | Description | Example |
|---------|-------------|---------|
| `help` or `?` | Show available commands | `help` |
| `tasks` or `📋` | List top 10 priority tasks | `tasks` |
| `✓1` or `done 1` | Complete task #1 from brief | `✓1` |
| `➕ Description` | Add new task | `➕ Call dentist @high` |

**Task Tags:** `@high`, `@urgent` → Impact 5, Urgency 5; `@low` → Impact 2, Urgency 2

**Implementation Summary:**
1. **Database**: Add `brief_tasks` table (migration 4) to map task numbers to IDs
2. **Daily Brief**: Store task IDs when sending brief (24-hour validity)
3. **Command Parser**: New package `internal/chat/commands.go` (~200 lines)
4. **Webhook**: Add `POST /api/chat/webhook` endpoint (no auth)
5. **Google Cloud**: Configure Chat app with webhook URL `https://your-domain.com/api/chat/webhook`

**Files to Create/Modify:**
- NEW: `internal/chat/commands.go` (command parser)
- NEW: `docs/CHAT_SETUP.md` (setup guide)
- MODIFY: `internal/db/migrations.go` (add migration 4)
- MODIFY: `internal/db/models.go` (add `StoreBriefTaskMappings`, `GetTaskIDFromBriefNumber`)
- MODIFY: `internal/google/auth.go` (pass database to `NewClients`)
- MODIFY: `internal/google/chat.go` (add DB field, store task mappings in `SendDailyBrief`)
- MODIFY: `internal/api/server.go` (add command handler, webhook route)
- MODIFY: `internal/api/handlers.go` (add `handleChatWebhook`)
- MODIFY: `cmd/agent/main.go`, `cmd/test-priority/main.go`, `cmd/test-task-match/main.go` (pass database param)

**Database Schema:**
```sql
CREATE TABLE IF NOT EXISTS brief_tasks (
    id VARCHAR PRIMARY KEY,
    task_number INTEGER NOT NULL,
    task_id VARCHAR NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX idx_brief_tasks_created ON brief_tasks(created_at);
```

**Testing:**
```bash
# Local test
curl -X POST http://localhost:8081/api/chat/webhook \
  -H "Content-Type: application/json" \
  -d '{"type":"MESSAGE","message":{"text":"help"},"user":{"displayName":"Test"}}'

# Live test in Google Chat
1. Send daily brief (stores task mappings)
2. In DM with bot: `help`, `tasks`, `✓1`, `➕ Test @high`
```

**Migration Note:** If migration 4 was already applied, the `brief_tasks` table exists but is unused until code is re-applied. No conflicts will occur.

**Next Steps (when domain ready):**
1. Set up HTTPS on custom domain → focus-agent server
2. Apply code changes from `PLAN.md`
3. Configure Google Chat app webhook URL
4. Test commands
5. Enjoy interactive task management!

---

### Google Tasks Two-Way Sync
**Priority:** Medium
**Status:** Planned

Sync AI-generated tasks with Google Tasks for better cross-platform task management.

**Functionality:**
- Create a dedicated Google Tasks list called "Focus Agent AI Tasks" or similar
- Sync AI-extracted tasks to this list automatically
- Allow editing tasks in Google Tasks (title, due date, completion)
- Sync changes back to local database
  - When a task is completed in Google Tasks, mark it completed locally
  - When a task is edited in Google Tasks, update the local copy
  - When a task is deleted in Google Tasks, soft-delete locally
- Preserve AI metadata (score, source thread, etc.) in local DB
- Handle conflicts (e.g., task edited in both places)

**Benefits:**
- Access tasks from mobile devices
- Use Google Tasks widgets and apps
- Leverage Google Calendar integration
- Sync across devices automatically
- Better for teams who already use Google Workspace

**Technical Approach:**
1. Add Google Tasks write scope to OAuth
2. Create sync service similar to Gmail/Drive sync
3. Map local task fields to Google Tasks API fields
4. Store Google Tasks ID in local task record for matching
5. Implement bidirectional sync with conflict resolution
6. Add sync schedule (e.g., every 5 minutes)

**Considerations:**
- Google Tasks API has limited fields (no priority, stakeholder, etc.)
- Need to decide what to do with non-AI tasks
- Handle Google Tasks limitations (no rich formatting)
- Quota limits for Google Tasks API

---

## Ideas / Backlog

### Granola Meeting Transcription Integration
Integrate with Granola (or similar meeting transcription tools) and Gemini to automatically extract tasks from meeting summaries.

**Problem:** Meeting notes and action items from Granola transcriptions need to be manually added to task lists.

**Solution:**
- Monitor for new Granola meeting summaries (via API, file watching, or email notifications)
- Parse AI-generated meeting summaries that include action items
- Extract tasks with assignees, deadlines, and context
- Automatically create tasks in Focus Agent
- Link tasks back to meeting transcript/summary for context
- Detect user's name variations to filter only user's tasks

**Benefits:**
- Zero friction capture of meeting action items
- Tasks automatically prioritized based on strategic alignment
- Full context from meeting transcript always available
- No manual data entry needed
- Works with Granola, Gemini, or other AI meeting tools

**Technical Approach:**
1. Add integration options: API webhook, email parsing, or file watching
2. Parse action items from AI summaries (similar to email thread parsing)
3. Map meeting attendees to stakeholders
4. Extract due dates from "by Friday" or similar phrases
5. Link to source meeting (calendar event ID or transcript URL)

### Email Auto-Reply Suggestions
Proactively suggest replies for common email patterns (meeting confirms, simple questions, etc.)

### Weekly Review
Generate a weekly summary of completed tasks, priorities, and progress

### Smart Reminders
Remind about tasks based on context (e.g., "You have a meeting with X in 1 hour, remember to review Y")

### Meeting Prep Automation
Automatically generate meeting prep when calendar events are detected

### Document Insights
Summarize changes in Google Docs you have access to

---

## Completed Features

### Task Extraction Filtering (2025-01-08)
- Updated AI prompt to only extract tasks for the user
- Added post-extraction filtering by stakeholder
- Added cleanup command for existing incorrect tasks
