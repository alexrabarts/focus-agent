# Focus Agent - Future Features

## Next Actions (as of October 26, 2025)

### 1. Prep Interactive Chat Commands Implementation
- Finalize HTTPS + custom domain routing so the `POST /api/chat/webhook` endpoint can be exposed publicly.
- Prototype the command parser (`internal/chat/commands.go`) behind a feature flag and write handler scaffolding that no-ops until the domain cutover is complete.
- Draft the `docs/CHAT_SETUP.md` guide while the flow is fresh, capturing OAuth scope requirements, webhook registration steps, and testing commands we will enable first.

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

### Multi-User Google Chat Support
**Priority:** Low
**Status:** Planned

Enable Focus Agent to operate as a shared Chat app across multiple Google Workspace users while keeping data and automations isolated per account.

**Key Workstreams:**
1. Extend OAuth onboarding to capture and persist per-user credentials plus authorized email metadata.
2. Store per-user DM space mappings (space ID, thread keys) in the database and update Chat client to resolve per request.
3. Refactor scheduler and planner workflows to run within a user context (tasks, briefs, follow-ups).
4. Implement per-user command handling and outbound rate limiting.
5. Add admin tooling to invite/remove users and monitor usage.

**Considerations:** quota impact, background job isolation, revocation flows, and migration path from the current single-user configuration.

---

## Ideas / Backlog

### Automated Receipt Upload Detection
**Priority:** Low
**Status:** Idea

Automatically detect and deduplicate expense receipt upload reminder tasks.

**Problem:** Multiple "Upload missing receipts via Spendesk" tasks are extracted from reminder emails, creating duplicate tasks that clutter the task list.

**Solution:**
- Detect receipt upload tasks by pattern matching (e.g., "upload.*receipt", "spendesk.*upload")
- Consolidate duplicate receipt tasks into a single task
- Track which specific receipts are missing (if mentioned in emails)
- Mark as completed when Spendesk confirmation email is detected
- Optionally: Integrate with Spendesk API to auto-check upload status

**Benefits:**
- Cleaner task list without duplicate reminders
- Automatic task completion when receipts are uploaded
- Less manual task management overhead
- Better visibility into pending expense submissions

**Technical Approach:**
1. Add post-extraction deduplication rule for receipt upload tasks
2. Parse email content for specific receipt identifiers (dates, amounts)
3. Monitor for Spendesk confirmation emails to auto-complete
4. Consider Spendesk API integration for real-time status

### Normalize Task Scores to Percentages
**Priority:** Low
**Status:** Planned

Normalize all task scoring fields to display as consistent percentages instead of raw numeric values.

**Problem:** Task calculated scores show values like 1.96 but display as 2.0 in the task list, creating confusion about precision and scale.

**Solution:**
- Normalize all scoring fields to 0-100% scale
- Affected fields: calculated score, impact, urgency, strategic alignment, effort, stakeholder
- Update TUI display formatting
- Update database queries to return normalized values
- Ensure consistency across API endpoints

**Benefits:**
- Clearer understanding of task priority (e.g., 85% vs 1.96)
- Consistent scale across all metrics
- More intuitive for users to understand relative priorities
- Better visual representation in TUI

### Fix Google Chat Hyperlinks
**Priority:** Medium
**Status:** Known Issue

Ensure daily brief source links render correctly inside Google Chat messages. Current markdown formatting renders as literal text. Investigate Chat markdown/link rules, adjust formatting, and update testing to confirm clickable links in both text and card messages.

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

### Ollama LLM Integration (2025-10-26)
- Added Ollama as primary LLM provider with Mistral 7B model
- Implemented hybrid fallback chain: Ollama → Claude CLI → Gemini Flash
- Fast local inference (2.4s response time) with zero cost
- All three providers initialized and operational in production
- Updated configuration in both local and production environments
- Task enrichment now uses Ollama first, falling back to Claude/Gemini if unavailable

### Daily Brief DM Space Delivery (2025-10-26)
- Validated daily brief delivery to Google Chat DM space
- Configured space ID: `spaces/oF73IiAAAAE`
- Created `find-chat-space` tool to discover bot DM spaces
- Confirmed briefs are delivered successfully without webhook fallback

### Google Chat Link Rendering Fix (2025-10-26)
- Fixed hyperlink rendering in Google Chat messages
- Changed from markdown `[label](url)` to Google Chat native `<url|label>` syntax
- Links now render as clickable hyperlinks in daily briefs
- Applied to task source links in `internal/google/chat.go`

### Task Enrichment Backfill Workflow (2025-10-26)
- Documented enrichment process in CLAUDE.md
- Verified LLM strategy: Ollama (primary) → Claude CLI → Gemini (fallback)
- Added runbook for running enrichment on demand
- Tested enrichment command successfully (0 tasks needed enrichment)

### Task Extraction Filtering (2025-01-08)
- Updated AI prompt to only extract tasks for the user
- Added post-extraction filtering by stakeholder
- Added cleanup command for existing incorrect tasks
