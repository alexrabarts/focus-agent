# Focus Agent - Future Features

## Planned Features

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
