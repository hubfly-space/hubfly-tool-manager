# TODO

## Done
- [x] Define tool JSON configuration schema and loader
- [x] Build SQLite storage for tool version/update history
- [x] Implement PM2 integration (install checks, start/stop/restart/status)
- [x] Implement tool updater with backups (keep last 3)
- [x] Implement self-update workflow (without tracking manager version)
- [x] Build HTTP API endpoints
- [x] Build CLI commands mapped to API/actions
- [x] Handle first-run unmanaged tools gracefully
- [x] Add defensive validation and panic recovery
- [x] Write README with setup, curl, and CLI usage

## Next hardening
- [ ] Add integration tests with PM2 mocks
- [ ] Add auth/token protection for HTTP endpoints
- [ ] Add structured metrics endpoint
- [ ] Add lock file to prevent multiple manager instances
