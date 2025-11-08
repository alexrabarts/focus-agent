# Known Issues

This document tracks current issues, blockers, and in-progress work.

## Status Indicators

- 🚧 **IN PROGRESS** - Actively being worked on
- 🛑 **BLOCKED** - Cannot proceed without external dependency
- ✅ **RESOLVED** - Issue has been fixed
- 💡 **WORKAROUND** - Temporary solution in place

## Format

Each issue should include:
- **Issue** - Clear description of the problem
- **Status** - Current state with emoji
- **Impact** - Who/what is affected
- **Next Steps** - What needs to happen
- **Related Files** - Relevant code locations

---

## Front API Integration - Multi-Inbox Limitation

**Issue:** Front integration only enriches conversations from personal inbox (alex@techspace.co - `inb_7mkjy`). Many work emails exist in shared team inboxes (Support, Events, etc.) or other teammates' inboxes and won't be enriched.

**Status:** 💡 **WORKAROUND** - Intentionally limited to personal inbox only for now

**Impact:**
- ~90% of Gmail threads don't match Front conversations (they're in other inboxes)
- Front enrichment (comments, tags, status) only works for personal inbox emails
- Shared team inbox conversations are not tracked

**Why It's This Way:**
- User preference: Only interested in personal inbox for now
- Reduces noise from shared inbox conversations that may not be relevant
- Simpler to reason about initially

**Next Steps (Future Enhancement):**

If multi-inbox support is needed later:

1. **Option A: Multi-Inbox Config**
   ```yaml
   front:
     inbox_ids:
       - "inb_7mkjy"  # Personal
       - "inb_7kczy"  # Support
       - "inb_7kgi6"  # Events
   ```

2. **Option B: Use Inbox-Specific API**
   Instead of global search, query specific inbox:
   ```
   GET /inboxes/{inbox_id}/conversations
   ```
   More efficient, avoids cross-inbox noise.

3. **Option C: Filter After Search**
   After global search, check conversation's inbox membership via:
   ```
   GET /conversations/{conv_id}/inboxes
   ```
   Filter out conversations not in allowed inboxes.

**Related Files:**
- `internal/front/client.go` - Search and linking logic
- `internal/scheduler/scheduler.go:enrichWithFront()` - Enrichment process
- `.agent/tasks/front-integration.md` - Implementation plan

**Technical Notes:**
- Front API rate limit: 100 requests/minute (Professional plan)
- Current batch size: 25 threads × 3 requests = 75 requests per batch
- Search is global by default (`/conversations/search`)
- No way to filter search by inbox in the API query itself

