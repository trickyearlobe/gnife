# Testing

* `internal/chefserver`: an `httptest`-backed in-memory Chef server that
  verifies v1.3 signatures against registered public keys and implements the
  endpoints in [commands.md](commands.md) plus sandboxes/bookshelf. Used for round-trip tests:
  backup → restore into a second fake org → deep-compare; copy `--deps` of a
  node whose role references cookbooks with metadata dependencies.
* Unit tests: credentials parser (each syntax form, inline PEM, relative key
  path), version constraints, run-list expansion with env_run_lists, path
  sanitisation, config atomic write.
* Live tests, opt-in: `GNIFE_LIVE_PROFILE=<profile> go test ./... -run Live`
  against a real server (read-only unless `GNIFE_LIVE_WRITE_ORG` is set).
