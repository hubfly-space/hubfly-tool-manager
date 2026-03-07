# Progress

## 2026-03-08
- Added project tracking documents (`todo.md`, `progress.md`).
- Implemented JSON configuration schema + validation for manager and tools.
- Added SQLite storage and migrations for tool version/update history.
- Added resilient PM2 integration with install check and process lifecycle operations.
- Implemented tool service core:
  - provisioning (clone/install/start)
  - manual update flow with backups
  - backup retention (last 3 per tool)
  - graceful version probing fallback
  - tool history retrieval
  - manager self-update path (excluded from tool version history)
- Implemented robust HTTP API with panic recovery and server timeouts.
- Implemented CLI (`htm`) for all core management operations.
- Added full README documentation with curl and CLI examples.
- Added rollback feature:
  - list backup snapshots per tool
  - restore latest or selected backup via API/CLI
  - pre-rollback safeguard snapshot before restore
