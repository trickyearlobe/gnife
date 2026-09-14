# GNIFE

Gnife is a command line utility that provides fast access to the Progress Chef Infra Server API.

## Raw API access

* The CLI must be able to submit raw GET, PUT, POST, DELETE, HEAD requests to the Chef server API
* Responses and errors are always returned in JSON format.

## Multiple organisations

* Credentials come from the standard Chef `~/.chef/credentials` file; each profile
  identifies a server + organisation. Every command accepts `--profile`.
* Commands that move data take `--from` and `--to` profiles.
* A `config` command stores defaults (default profile, source, dest, concurrency, ...)
  so profiles need not be repeated on every invocation.

## Object coverage

* Every Chef Infra Server object type can be listed, shown, created, updated,
  deleted and copied through a uniform noun-verb command structure (cobra).
* Server-level objects (users, organisations) are covered too; they require a
  superuser (pivotal) profile.

## Backup and restore

* A whole organisation can be backed up to disk and restored, in a layout
  compatible with `knife ec backup` / `knife ec restore`.
* The on-disk organisation directory may be renamed before a restore; the
  restore target is always the organisation the destination profile points at.

## Copying between organisations

* Objects can be copied between organisations, on the same or different servers.
* `--deps` also copies the object's dependencies: a role's cookbooks (and
  nested roles), a node's environment, roles, cookbooks or policy + cookbook
  artifacts, a policy's cookbook artifacts, a cookbook's dependency cookbooks.
* Existing destination objects are skipped unless `--overwrite` is given.
  `--dry-run` shows what would be copied.

## Performance

* Concurrent listing, fetching, uploading and downloading with a configurable
  worker count; HTTP keep-alive connection reuse.

## Supply chain

* External Go dependencies are kept to the bare minimum (cobra + pflag).
  Everything else — Chef request signing, credentials parsing, config, TOML
  subset, tar/gzip — uses the Go standard library.
* GitHub Actions are pinned to commit SHAs. `make deps-check` fails the build
  if `go.mod` grows beyond an allowlist.

## Build and release

* A Makefile builds, tests, lints and cross-compiles the binary.
* Version numbers come from git tags, which are semver (`vX.Y.Z`). The binary
  reports the tag it was built from; untagged local builds report
  `git describe` output. Nothing in the source carries a version.
* The GitHub Actions pipeline runs **only** when a tag is pushed. It runs the
  tests on linux, macOS and Windows, builds for all three on x86_64 and
  aarch64, and publishes the artifacts with checksums as a GitHub release
  named after the tag.
