# Credentials and configuration

## Credentials (`internal/credentials`)

Parses `~/.chef/credentials` (override: `CHEF_CREDENTIALS_FILE`, or config key
`credentials`). Supported TOML subset — exactly what chef-config emits and
documents:

* `[profile]` and `[profile.knife]` tables (the latter parsed and ignored)
* `key = "basic string"` with `\"`, `\\`, `\n` escapes
* `key = 'literal string'`
* `key = """multi-line"""` (inline PEM private keys)
* `key = true|false`
* `#` comments, blank lines

Anything else is a parse error naming the line.

Profile fields: `chef_server_url`, `client_name` (alias `node_name`),
`client_key` (path or inline PEM; relative paths resolve against the
credentials file's directory), `ssl_verify_mode`, `validation_client_name`,
`validation_key`.

Profile selection order:

1. `--profile` / `--from` / `--to`
2. `CHEF_PROFILE`
3. `profile` / `source` / `dest` from gnife config
4. `~/.chef/context`
5. `default`

Field overrides from env, as knife: `CHEF_SERVER_URL`, `CHEF_NODE_NAME`,
`CHEF_CLIENT_KEY`. Private key bytes are read at client construction and
zeroed afterwards.

## Config (`internal/config`)

`~/.gnife/config.json` (override: `GNIFE_CONFIG`). Flat string map:

| key | meaning |
|---|---|
| `profile` | default `--profile` |
| `source` | default `--from` |
| `dest` | default `--to` |
| `concurrency` | worker count (default 16) |
| `credentials` | path to credentials file |
| `output` | `json` (default) or `names` |

```
gnife config set source prod
gnife config set dest staging
gnife config get dest
gnife config unset dest
gnife config list
gnife config path
```

Writes are atomic (temp file + rename, mode 0600).
