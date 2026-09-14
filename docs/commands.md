# Command tree

Noun-verb throughout. Aliases: `ls`→`list`, `get`→`show`, `rm`→`delete`.

```
gnife config    get|set|unset|list|path
gnife profile   list | show [NAME] | test [NAME]        # test = GET /_status + /users/me-style auth check
gnife raw       get|put|post|delete|head PATH [-d BODY|@file|-]
gnife search    INDEX QUERY [--rows N] [--start N] [--partial key=a.b.c ...]
gnife status                                            # /_status
gnife version

gnife backup    --dir DIR [-p PROFILE] [--only KINDS] [--skip KINDS] [--skip-users] [--skip-acls] [--archive] [--in-use]
gnife restore   --dir DIR|FILE.tar.gz [-p PROFILE] [--org DIRNAME] [--create-org] [--overwrite] [--force] [--purge] [--dry-run] [--only|--skip KINDS] [--skip-users] [--skip-acls]
gnife clone     --from A --to B [same flags as restore] [--in-use]  # backup to temp dir, restore, delete; --in-use goes server to server
gnife serve     --dir DIR [--listen HOST:PORT] [--org NAME] [--tls | --tls-cert F --tls-key F] [--no-log]   # see serve.md

gnife node        list|show|create|update|edit|delete|copy
gnife role        list|show|create|update|edit|delete|copy
gnife environment list|show|create|update|edit|delete|copy
gnife databag     list|show BAG|create|delete|copy                  # copy always carries the items
gnife databag item list | show BAG ITEM | create BAG -f | update BAG ITEM -f | edit | delete | copy BAG/ITEM
gnife cookbook    list [--all-versions] | show NAME [VER] | download NAME VER --dir D
                  | upload DIR [--freeze] [--force] | delete NAME VER | freeze NAME VER | copy NAME[/VER]
gnife artifact    list | show NAME ID | download | upload DIR --identifier ID | delete | copy NAME/ID   # cookbook_artifacts
gnife policy      list | show NAME [REV] | create -f | delete NAME/REV | copy NAME/REV
gnife policygroup list | show G | create | update | edit | assign G POLICY REV | unassign G POLICY | delete G | copy G
gnife client      list|show|create|update|delete|copy
gnife client key  list|show|add|delete
gnife user        list|show|create|update|delete|copy                             # pivotal
gnife user key    list|show|add|delete
gnife group       list|show|create|update|delete|copy | add G MEMBER... | remove G MEMBER...
gnife container   list|show|create|delete
gnife acl         show KIND [NAME] | update KIND [NAME] -f | edit KIND [NAME] | copy KIND [NAME]   # KIND organization needs no name
gnife org         list|show|create [NAME] [--full-name N] [--validator-keyfile F (default ~/.chef/NAME-validator.pem)]|update|delete | member {list ORG | add ORG USER | remove ORG USER}   # pivotal; create prompts for what is missing
```

Global flags: `-p/--profile`, `--config`, `--concurrency`, `-o/--output`,
`-q/--quiet`, `--debug`.

`create`/`update` take `-f FILE` (`-` for stdin). `edit` fetches, opens
`$VISUAL`/`$EDITOR` (`os/exec`), diffs, PUTs. `copy` flags: `--from`, `--to`,
`--deps`, `--acl`, `--overwrite`, `--force` (replace frozen cookbooks too),
`--dry-run`, `--all` (every object of that kind), and `--environment` on
`role`/`node` for depsolving. `--from`/`--to` default to config
`source`/`dest`; if neither the flag nor the config supplies one, the command
errors rather than guessing. Composite names are `NAME/VERSION`,
`NAME/REVISION`, `BAG/ITEM`; a cookbook given without a version copies the
latest.

### Kind registry (`internal/kinds`)

The verbs above are generated from one table so every object type behaves
identically. Each Kind supplies:

```go
type Kind struct {
    Name      string            // "node"
    Plural    string            // "nodes"    (API path and backup dir)
    Path      string            // "/nodes"   or "/users" with ServerLevel
    ServerLevel bool
    Composite bool              // name has two parts: cookbook/VER, policy/REV, bag/item, artifact/ID
    List      func(ctx, *chef.Client) ([]ID, error)
    Get       func(ctx, *chef.Client, ID) (json.RawMessage, error)
    Put       func(ctx, *chef.Client, ID, json.RawMessage, PutOpts) error   // create or replace
    Delete    func(ctx, *chef.Client, ID) error
    Deps      func(ctx, *chef.Client, ID, json.RawMessage) ([]Ref, error)   // see copy-and-dependencies.md
    Order     int               // restore order, see backup-and-restore.md
}
```

Cookbooks and artifacts override `Get`/`Put` with directory-based transfer
(manifest + files) rather than a single JSON body.

| Kind | Endpoint | Notes |
|---|---|---|
| container | `/containers` | |
| group | `/groups` | strip `orgname` on write; membership set in a second pass |
| client | `/clients` | copy carries public keys via `/clients/N/keys` |
| environment | `/environments` | `_default` never deleted/overwritten |
| cookbook | `/cookbooks/N/V` | composite, files |
| artifact | `/cookbook_artifacts/N/ID` | composite, files |
| role | `/roles` | |
| databag | `/data/B` and `/data/B/I` | composite |
| node | `/nodes` | |
| policy | `/policies/N/revisions/R` | composite; created via `PUT /policy_groups/G/policies/N` or `POST /policies/N/revisions` |
| policygroup | `/policy_groups/G` | assignment map policy→revision |
| acl | `/KIND/N/_acl` (+ `/organizations/ORG/_acl`) | `PUT /_acl/{create,read,update,delete,grant}` per permission |
| user | `/users` (server) | pivotal |
| org | `/organizations` (server) | pivotal |
