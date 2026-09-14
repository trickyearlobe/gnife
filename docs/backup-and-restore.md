# Backup and restore

### Layout — knife-ec-backup compatible

```
DIR/
  users/<name>.json                  server-level (pivotal only; skipped otherwise)
  user_acls/<name>.json
  organizations/<org>/
    org.json                         {name, full_name, guid}
    members.json                     [{"user": {"username": ...}}]
    invitations.json
    containers/<n>.json
    groups/<n>.json
    clients/<n>.json
    environments/<n>.json
    cookbooks/<name>-<version>/      files at their manifest path + metadata.json
    cookbook_artifacts/<name>-<identifier>/
    roles/<n>.json
    data_bags/<bag>/<item>.json
    nodes/<n>.json
    policies/<name>-<revision>.json
    policy_groups/<g>.json
    acls/organization.json
    acls/<plural>/<n>.json           actors/users/clients/groups per permission
```

gnife adds one file, `organizations/<org>/.gnife.json` (tool version, server
URL, API version, timestamp). knife ec restore ignores dotfiles.

`--archive` additionally writes `DIR.tar.gz` (`archive/tar` +
`compress/gzip`); `restore --dir` accepts either a directory or an archive.

### Renaming an organisation

The restore target is **always** the organisation in the destination
profile's URL. The directory under `organizations/` is only a source:

* `--org DIRNAME` picks it; with exactly one directory it is used
  automatically; otherwise a directory matching the target org name is used;
  otherwise error.
* `org.json`'s `name` is ignored. With `--create-org` (pivotal) the org is
  created under the *target* name with `full_name` from `org.json`; the new
  validator's private key is saved to `~/.chef/<org>-validator.pem`
  (`--validator-keyfile` to choose; an existing file blocks creation).
* Object bodies do not embed the org name, except `groups/*.json`'s
  `orgname`, which is stripped on write.

So `mv organizations/prod organizations/prod-copy` followed by
`gnife restore --dir DIR --to prod-copy` is the whole rename procedure.

### Restore order

```
 1 org (create if --create-org)         8 cookbook_artifacts
 2 containers                           9 roles
 3 groups (shells, no members)         10 data_bags + items
 4 clients (+ keys)                    11 nodes
 5 users / members / invitations       12 policies
 6 environments                        13 policy_groups
 7 cookbooks                           14 groups (membership pass)
                                       15 acls (organization, then per object)
```

Within each step objects are transferred through the worker pool. `--purge`
deletes destination objects of each restored kind that are absent from the
backup (never `_default`, never the built-in groups/containers, never the
restoring client itself, never USAGs).

The `pivotal` user is backed up like any other but is never restored: with
`--overwrite` that would replace the destination superuser's key. The
source's `<org>-validator` client *is* restored (its key keeps working for
already-bootstrapped nodes); after a rename it sits alongside the
destination's own validator.

USAGs — the 32-hex-digit *user-specific access groups* the server creates
for each organisation member — are hidden from `group list` and excluded
from backup, restore, copy and purge: the destination creates its own when a
user is associated, so copying them would only leave meaningless groups
behind.

### Backup

Kinds are listed concurrently, then all objects fetched through the pool.
Cookbook file downloads are deduplicated by checksum. Users/user ACLs are
attempted and skipped with a single warning on 403.
