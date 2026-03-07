# TODO

## Core
- [ ] Define tool JSON configuration schema and loader
- [ ] Build SQLite storage for tool version/update history
- [ ] Implement PM2 integration (install checks, start/stop/restart/status)
- [ ] Implement tool updater with backups (keep last 3)
- [ ] Implement self-update workflow (without tracking manager version)
- [ ] Build HTTP API endpoints
- [ ] Build CLI commands mapped to API/actions
- [ ] Handle first-run unmanaged tools gracefully

## Stability
- [ ] Add defensive validation and error handling
- [ ] Add panic recovery for HTTP server
- [ ] Add retries/timeouts for external commands

## Docs
- [ ] Write README with setup, curl, and CLI usage
- [ ] Keep progress log current
