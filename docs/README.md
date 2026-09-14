# gnife design

`gnife` is a single static Go binary for working with one or many Chef Infra
Server organisations. It is deliberately boring: cobra for the command tree,
the Go standard library for everything else.

Requirements are in [`../Requirements specification.md`](../Requirements%20specification.md).
These documents are the design that satisfies them.

| Document | What it covers |
|---|---|
| [dependencies.md](dependencies.md) | Why cobra is the only external module, and how that is enforced |
| [architecture.md](architecture.md) | Package layout, the Chef API client, cookbook upload, concurrency |
| [credentials-and-config.md](credentials-and-config.md) | `~/.chef/credentials` parsing, profile selection, `~/.gnife/config.json` |
| [commands.md](commands.md) | The full command tree and the Kind registry that generates it |
| [copy-and-dependencies.md](copy-and-dependencies.md) | `copy`, `--deps`, how the cookbook closure is resolved |
| [backup-and-restore.md](backup-and-restore.md) | knife-ec-backup layout, renaming organisations, restore order |
| [serve.md](serve.md) | `gnife serve`: a Chef Infra Server over a backup directory |
| [security.md](security.md) | Path sanitisation, key handling, TLS, build hardening |
| [testing.md](testing.md) | Fake server, unit tests, opt-in live tests |
| [build-and-release.md](build-and-release.md) | Semver tags, Makefile, `bump-*-push`, tag-only GitHub Actions |
| [implementation-plan.md](implementation-plan.md) | The order to build it in |
