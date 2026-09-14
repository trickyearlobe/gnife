# Dependency policy

| Module | Why | Transitive |
|---|---|---|
| `github.com/spf13/cobra` | command tree, help, completion | `spf13/pflag`, `inconshreveable/mousetrap` (Windows only) |

That is the whole list. In particular:

* **No viper.** The original cobra-cli skeleton pulled it in; it brings ~12
  modules (afero, cast, mapstructure, fsnotify, go-toml, yaml, ...) for a job
  that is 30 lines of `encoding/json`.
* **No go-chef.** The Chef client is lifted from
  `chef-migration-metrics/internal/chefapi` (ours, stdlib only) into
  `internal/chef`.
* **No TOML library.** `~/.chef/credentials` uses a small, fixed subset of
  TOML; a 150-line parser covers it (see [credentials-and-config.md](credentials-and-config.md)).
* **No yaml.** Output is JSON; config is JSON.

`deps-allowlist.txt` lists the permitted module paths. `make deps-check`
resolves every package the binary imports (`go list -deps`, on Windows,
the platform with the most) to its module and fails on anything not listed.
It deliberately does not use `go list -m all`: cobra's `go.mod` also names
go-md2man, blackfriday and yaml for its optional doc generator, which are
never compiled into or downloaded for gnife. `go mod verify` runs first so
the checksums of what *is* used are confirmed. CI runs both.

The module path is `github.com/trickyearlobe/gnife`.
