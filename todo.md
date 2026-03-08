# TODO

## Done
- [x] PM2 process management (install check, start/stop/restart)
- [x] HTTP API + CLI for tool lifecycle
- [x] SQLite version history and update tracking
- [x] Backup retention (last 3) and rollback
- [x] Default port set to `10000`
- [x] Database-driven tool registry (no tool `config.json`)
- [x] Register endpoint for new tools with URL/checksum/version command/args
- [x] Per-tool folder convention and binary chmod+x handling
- [x] Per-tool cleanup endpoint (tool dir + backups + db)
- [x] Linux release workflow and one-line installer with systemd auto-restart

## Next hardening
- [ ] Add auth/token protection for HTTP endpoints
- [ ] Add integration tests with mocked PM2 and download server
- [ ] Add per-tool concurrency locks
- [ ] Add optional signed binary verification
- [ ] Add explicit restore endpoint to pick backup + dry-run validation
