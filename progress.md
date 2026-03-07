# Progress

## 2026-03-08
- Added project tracking documents (`todo.md`, `progress.md`).
- Implemented PM2 manager with API + CLI + SQLite history + backups + rollback.
- Added backup listing and rollback endpoint/CLI.
- Changed default port to `10000`.
- Reworked architecture to database-first tool registry (no tool config file required):
  - Added `POST /tools/register` for dynamic tool registration
  - Tools are stored in SQLite (`tools` table)
  - Added per-tool folders under `tools/<name-with-dashes>`
  - Added binary download flow with `chmod +x`
  - Added optional SHA256 checksum validation (skipped when not provided)
  - Added `POST /tools/{name}/cleanup` to remove one tool safely
