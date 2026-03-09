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
- Added Linux packaging and release automation:
  - GitHub workflow for Linux (`amd64`, `arm64`) release artifacts + checksums
  - one-line installer script for `/hubfly-tool-manager`
  - systemd service with `Restart=always` (manager not run under PM2)
- Improved operations:
  - installer now links `htm` and `hubfly-tool-manager` into `/usr/local/bin`
  - self-update no longer requires working directory; it auto-updates from GitHub releases
- Added API token security:
  - all endpoints now require token except public manager version endpoint
  - token initialized/overwritten locally using `htm init` (no API endpoint for token setup)
  - CLI auto-loads token from `/hubfly-tool-manager/.token` for protected requests
- Added lockdown protection:
  - 10 invalid-token attempts trigger API lockdown for protected endpoints
  - lockdown state persisted in `/hubfly-tool-manager/.lockdown.json`
  - unlock available only via local CLI command `htm unlock`
