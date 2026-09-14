# gnife

A fast command line tool for working with one or many Progress Chef Infra
Server organisations: manipulate every object type, back up and restore whole
organisations, copy objects — with their dependencies — between
organisations, and serve a backup as a working Chef Infra Server.

Single static Go binary. One external dependency (cobra).

> Status: implemented and tested against an in-memory Chef server and, read-only,
> a live one; see [docs/implementation-plan.md](docs/implementation-plan.md)
> for known limits and what is not yet verified. No release has been tagged.

## Installation

Download a binary for your platform from the
[releases page](https://github.com/trickyearlobe/gnife/releases)
(linux, macOS and Windows; x86_64 and aarch64), or build from source:

```
go install github.com/trickyearlobe/gnife@latest
```

## Quick start

gnife authenticates with the profiles in your existing `~/.chef/credentials`
file — no separate setup.

```
gnife profile list                          # profiles found in ~/.chef/credentials
gnife node list -p prod                     # any command takes --profile
gnife config set source prod                # stop repeating --from/--to
gnife config set dest staging
gnife role copy webserver --deps            # role + nested roles + their cookbooks
gnife node copy web-01 --deps --dry-run     # show the plan, change nothing
gnife backup --dir ./prod-backup            # whole org, knife-ec-backup layout
gnife restore --dir ./prod-backup -p dr     # into whichever org the profile points at
gnife raw get /nodes                        # anything the API exposes
gnife serve --dir ./prod-backup             # serve the backup as a Chef server (knife, chef-client work)
```

`gnife help` and `gnife <noun> --help` list every command; the full tree is in
[docs/commands.md](docs/commands.md).

## Documentation

* [Requirements specification.md](Requirements%20specification.md) — what
  gnife must do.
* [docs/](docs/README.md) — how it does it: architecture, credentials and
  config, commands, copying, backup/restore, security, testing, release
  process.

## Building and releasing

```
make build          # ./gnife for this machine
make test
make bump-patch-push   # tag the next semver version and push it; CI builds the release
```

Details in [docs/build-and-release.md](docs/build-and-release.md).

## Contributing

PRs are welcome: fork, branch, raise a PR. `make lint test deps-check` must
pass (`make test-race` too if you have a C toolchain); new external dependencies will not be accepted without a discussion
first (see [docs/dependencies.md](docs/dependencies.md)).

## License

Apache 2.0 — see [LICENSE](LICENSE).
