# Architecture

## Package layout

```
main.go                         calls cmd.Execute()
cmd/                            cobra wiring only — flags in, internal calls out
  root.go                       global flags, profile/config resolution, output
  config.go profile.go raw.go search.go status.go version.go
  backup.go restore.go clone.go
  node.go role.go environment.go databag.go cookbook.go artifact.go
  policy.go policygroup.go client.go user.go group.go container.go acl.go org.go
  generic.go                    builds list/show/create/update/edit/delete/copy
                                verbs for any registered Kind
internal/
  chef/                         API client (lifted): signing, transport, APIError,
                                retry, JSON Get/Put/Post/Delete, depsolver, sandbox
  chef/cookbook/                manifest <-> directory, download, upload
  credentials/                  ~/.chef/credentials parser + profile resolution
  config/                       ~/.gnife/config.json
  kinds/                        Kind registry (see commands.md)
  deps/                         run-list expansion, version constraints, closure
  transfer/                     copy engine: source -> dest with deps/dry-run
  backup/                       knife-ec-backup layout reader/writer, ordering
  chefserver/                   Chef Infra Server implementation (tests in memory, serve on disk)
  serve/                        gnife serve: on-disk store in the backup layout, TLS, bootstrap
  cli/                          output, progress, exit codes
```

`cmd/` never talks HTTP directly; everything is testable without cobra.

## Chef API client (`internal/chef`)

Lifted verbatim where possible from `chefapi/client.go`:

* `ClientConfig`/`NewClient`, `parsePrivateKey` (PKCS#1 + PKCS#8),
  `newDefaultHTTPClient` (dial/TLS/response-header timeouts,
  `MaxIdleConnsPerHost` 32, HTTP/2), `signRequest` (v1.3, RSA-SHA256),
  `splitString`, `doRequest`, `APIError` (+`IsRetryable`/`IsNotFound`),
  `DoWithRetry`, `DownloadFileContent` (MD5/SHA-256 verified).

Changes on top:

* `X-Ops-Server-API-Version` becomes a constant `apiVersion = "2"` used in
  both the header and the canonical signing string. v2 gives cookbooks and
  cookbook artifacts the flat `all_files` manifest, which is one code path
  for download and upload instead of nine segments. Requires Chef Infra
  Server ≥ 12.17 (2017); anything older is out of scope.
* `X-Chef-Version: 18.0.0`, `User-Agent: gnife/<version>`.
* Generic JSON helpers: `Get(ctx, path, &out)`, `Put`, `Post`, `Delete`,
  `Head`, plus `Raw(ctx, method, path, body) ([]byte, int, error)` for the
  `raw` command.
* Trusted certs: every PEM in `~/.chef/trusted_certs/` is added to the
  `x509.CertPool` (this is what knife does); `ssl_verify_mode = ":verify_none"`
  maps to `InsecureSkipVerify` (noted under `--debug` only).
* Debug tracing (`--debug`) logs method, path, status, duration — never
  `X-Ops-Authorization-*`.
* Endpoints: everything in [commands.md](commands.md), plus
  `POST /environments/ENV/cookbook_versions` (depsolver),
  `POST /sandboxes`, `PUT /sandboxes/ID`, `GET /universe`.

The server URL from the profile includes `/organizations/ORG`; the client
strips it and stores `Org` so server-level paths (`/users`, `/organizations`)
can be addressed from the same client.

### Cookbook upload

```
1. walk cookbook dir → all_files entries with MD5 checksums, path, specificity
2. POST /sandboxes {"checksums": {md5: null, ...}}
3. for each checksum with needs_upload=true: PUT url (bookshelf, pre-signed:
   no Chef auth headers) with Content-Type application/x-binary, Content-MD5
4. PUT /sandboxes/ID {"is_completed": true}
5. PUT /cookbooks/NAME/VERSION  (or /cookbook_artifacts/NAME/IDENTIFIER)
   with manifest + metadata; ?force=true when --overwrite and frozen
6. optional PUT with "frozen?": true for --freeze
```

Step 3 is fanned out across the worker pool. Checksums are deduplicated
across cookbooks within one run so a restore of 200 cookbooks sharing a
`README.md` uploads it once.

## Concurrency and speed

* One `sync`-based worker pool (`internal/cli/pool.go`): bounded goroutines,
  context cancellation on first fatal error, per-item error collection so a
  single bad object does not abort a 10 000-node backup.
* Default 16 workers (`--concurrency`, config `concurrency`).
* Keep-alive transport with 32 idle connections per host.
* `DoWithRetry` on 429/5xx with backoff.
* Exit codes: 0 ok · 1 error · 2 usage · 3 partial (some items failed;
  details on stderr as JSON lines).
