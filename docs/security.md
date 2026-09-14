# Security considerations

* Cookbook file `path` values come from the server; they are rejected if
  absolute, contain `..` or `\`, or escape the cookbook dir (`filepath.Clean`
  + prefix check). Object names are validated the same way before becoming
  file names.
* Private keys: read once, held only in the client, zeroed from the config
  struct after parse; never logged.
* `--debug` never prints authorization headers or request bodies of key
  endpoints.
* TLS verification on by default; `:verify_none` in the profile disables it silently (it is the user's explicit choice); `--debug` notes it.
* Config and credentials files written 0600.
* Builds are `CGO_ENABLED=0 -trimpath` so binaries are static and
  reproducible; CI actions pinned by SHA; `go mod verify` + `deps-check` in CI.
