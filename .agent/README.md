# Focus Agent - Agent Documentation

This directory contains documentation specifically designed for AI agents (like Claude Code) to understand the project context, common issues, and operational procedures.

## Directory Structure

### `sop/` - Standard Operating Procedures
Documentation about recurring issues, debugging procedures, and lessons learned:
- **learnings.md** - Insights and lessons learned during development
- **issues.md** - Known issues and their resolution status
- **deployment.md** - Deployment procedures and operational runbooks

### `system/` - System Documentation
Technical documentation about architecture and dependencies:
- **architecture.md** - System design and component relationships (to be created)
- **database.md** - Database schema and migration notes (to be created)
- **dependencies.md** - External dependencies and their purposes (to be created)

### `tasks/` - Project Planning
Forward-looking documentation about features and roadmap:
- **roadmap.md** - Long-term vision and planned features
- **backlog.md** - Prioritized list of pending work (to be created)
- **prd.md** - Product requirements documents (to be created)

## Quick Links

- [Known Issues](sop/issues.md) - Current problems and blockers
- [Learnings](sop/learnings.md) - What we've discovered along the way
- [Deployment](sop/deployment.md) - Operational runbooks and procedures
- [Roadmap](tasks/roadmap.md) - Planned features and future direction
- [Database](system/database.md) - Schema documentation and queries

## How to Update

Use the `/update-doc` command to add new content:

```bash
/update-doc I learned that X requires Y
/update-doc Issue: Z is broken when W happens
/update-doc Feature idea: implement ABC
```

The command will automatically route content to the appropriate file based on context.

## Integration with CLAUDE.md

This `.agent/` directory complements the project's `CLAUDE.md` file:
- **CLAUDE.md** contains development conventions, coding standards, and workflow preferences
- **`.agent/`** contains operational knowledge, issues, and project planning

Both are designed to help AI agents work effectively on this codebase.
