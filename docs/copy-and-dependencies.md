# Copying objects between organisations

`copy` builds a **plan**: an ordered set of `(Kind, ID)` to transfer, then
executes it. `--dry-run` prints the plan as JSON and stops. Without
`--deps` the plan is just the named objects.

Dependency expansion per kind:

| Copying | `--deps` adds |
|---|---|
| node | `chef_environment`; run_list roles (recursive, honouring `env_run_lists` for the node's environment); cookbook closure of the expanded recipe list; **or**, for a Policyfile node, the `policy_group`/`policy_name` revision and its cookbook artifacts |
| role | roles in `run_list` and every `env_run_lists` (recursive); cookbook closure of the expanded recipes |
| environment | for each `cookbook_versions` constraint, the newest source version satisfying it |
| cookbook | metadata `dependencies`, resolved greedily newest-first against the source's version list, recursively |
| policy | every `cookbook_locks[*].identifier` as an artifact |
| policygroup | every assigned policy revision, then their artifacts |
| databag | all items (always, `--deps` or not) |
| group | member groups (recursive); member clients; users are checked in dest and dropped with a warning if absent |
| any + `--acl` | the object's ACL, copied last; actors/groups missing in dest are dropped with a warning |

**Cookbook closure** uses the server: expand roles client-side to a recipe
list, then `POST /environments/ENV/cookbook_versions {"run_list": [...]}` on
the *source*. The server's depsolver returns the exact cookbook versions it
would give a client, transitive dependencies included, respecting the
environment's constraints. This is both faster and more faithful than
re-implementing the solver. The manual constraint resolver (`>= > < <= = ~>`
over `x.y[.z]`) is only used where the depsolver cannot be — pinned cookbook
versions and environment constraints.

Execution order is the restore order of [backup-and-restore.md](backup-and-restore.md), so dependencies land
before dependants (artifacts before policies, cookbooks before roles, roles
and environments before nodes, everything before ACLs).

Conflict policy: destination object exists → skip and report, unless
`--overwrite`. Frozen cookbook versions are only replaced with `--overwrite
--force`. Same-server copies (two profiles, one host) are the common case;
users are never created, only associated when the destination profile has
the rights.

## Only what is in use

`backup --in-use` and `clone --in-use` apply the same expansion to **every
node** at once (`transfer.InUsePlan`): each node's environment, roles,
depsolved cookbook versions (or policy revision, artifacts and policy group)
are kept; everything a node cannot reach is left behind — old cookbook
versions above all, but also unreferenced roles, environments, policies and
artifacts. Containers, groups, clients and data bags are always included in
full, since their use cannot be read off the server. `clone --in-use` skips
the temporary directory and streams server to server; `--purge` does not
apply to it.

The plan's warnings double as a health report: every node whose run list
names a role or cookbook missing on the source is listed.

On a real organisation of 27 cookbook versions and 42 roles, 13 nodes
reached 7 versions and 1 role (16 MB → 5 MB on disk).
