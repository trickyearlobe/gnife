# Implementation status

Everything in the design is implemented and covered by tests against the
in-memory server (`internal/chefserver`), plus read-only checks against
a live Chef Infra Server 15 organisation.

| Area | Package | Status |
|---|---|---|
| Chef API client, v1.3 signing, retries, bookshelf | `internal/chef` | done |
| `~/.chef/credentials` parser, profile resolution | `internal/credentials` | done |
| gnife config | `internal/config` | done |
| Kind registry (all object types) | `internal/kinds` | done |
| Cookbook / artifact download, upload, copy | `internal/cookbook` | done |
| ACLs | `internal/acl` | done |
| Run-list expansion, depsolver, version constraints | `internal/deps` | done |
| Copy planner and executor (`--deps`, `--acl`, `--dry-run`) | `internal/transfer` | done |
| Backup, restore, purge, archive, org rename | `internal/backup` | done |
| Command tree | `cmd/` | done |
| Makefile, tag-only CI, `bump-*-push` | root, `.github/workflows` | done |
| Chef server implementation: auth 1.0–1.3, all endpoints, Solr-style search, API version negotiation | `internal/chefserver` | done |
| `gnife serve`: on-disk store, bootstrap, TLS | `internal/serve` | done; verified with Knife and Chef Infra Client 18 |

## Known limits

* Uploading a cookbook directory needs `metadata.json`; `metadata.rb` is
  Ruby and is not evaluated. Backups always contain one (gnife writes it
  from the server's metadata when the cookbook lacks it).
* Frozen state of cookbook versions is not part of the knife-ec-backup
  layout, so a restore does not re-freeze; `cookbook freeze` does.
* Users and organisation membership need a superuser profile; with an
  ordinary profile those steps are skipped with a warning, not an error.
* A group member (user) who is not a member of the destination organisation
  is dropped from the group with a warning. The same applies to ACL actors.
* Data bag items are copied verbatim; encrypted items stay encrypted with
  the source's secret.

## Not yet verified

* Live restore with `--purge` or `--create-org` (clone with and without
  `--overwrite` into a real organisation has been run and verified).
* Windows binaries are cross-compiled but the test suite has only been run
  on macOS and Linux.
