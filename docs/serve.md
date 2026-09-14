# gnife serve — a Chef Infra Server over a backup

`gnife serve --dir DIR` runs a Chef Infra Server whose state *is* a
directory in the knife-ec-backup layout. A backup taken with `gnife backup`
becomes a live organisation; every change a client makes is written back
into the directory immediately, so the directory is always a valid backup
of what is being served. Think chef-zero, but with real persistence, real
authentication and a real search engine.

```
gnife backup --dir ./prod -p prod          # take a backup
gnife serve  --dir ./prod                   # serve it on http://127.0.0.1:8889
gnife serve  --dir ./scratch --org dev      # a fresh server with an empty org
gnife serve  --dir ./prod --tls --listen 0.0.0.0:8443
```

## What it is for

* **Rehearsing a restore**: serve the backup, run `chef-client` and `knife`
  against it, and see whether the estate is coherent before touching a
  real server.
* **Reproducing an estate offline**: a customer's backup runs on a laptop.
* **Cookbook development** without infrastructure: `knife cookbook upload`,
  `chef-client -S http://127.0.0.1:8889/organizations/dev`.
* **gnife's own tests**: the same package (`internal/chefserver`) backs
  them in memory.

## Identity and keys

* **pivotal** — if the directory holds `users/pivotal.json` (a backup taken
  by a superuser does), the public key in it is used, so the `pivotal.pem`
  of the server the backup came from keeps working. Otherwise a superuser
  is generated and its key written to `DIR/pivotal.pem`.
* **Users and clients** from the backup authenticate with their existing
  private keys — the backup carries their public keys.
* **`--org NAME`** creates the organisation if absent, with an
  `NAME-validator` client whose key is written to `DIR/NAME-validator.pem`,
  ready for `knife bootstrap` / `chef-client` first runs.
* **New clients** registered through the API (chef-client's first run) get
  server-generated keys, as on a real server.

Signing protocols 1.0, 1.1, 1.2 and 1.3 are all accepted, so anything
from Chef 11 onwards can talk to it.

## What is served

Everything gnife, knife and chef-client use, verified against Chef Infra
Client and Knife 18: organisations, users, clients and keys, nodes, roles,
environments (including `/environments/ENV/cookbooks` and the depsolver at
`/environments/ENV/cookbook_versions`), cookbooks and artifacts with
sandboxes and bookshelf, `_latest`/`_recipes`, `/universe`, data bags,
policies and policy groups, groups, containers, ACLs, org membership and
association requests, search with partial search.

**Search** is a real query engine over the objects, implementing the Chef
subset of Solr syntax: `field:value`, wildcards, `"phrases"`,
`[a TO b]`/`{a TO b}` ranges, `AND`/`OR`/`NOT`/`-`/`+`, parentheses, bare
terms. Objects are indexed the way Chef's indexer does it — node
attributes merged by precedence, nested keys joined with `_` at every
suffix (`kernel_machine` and `machine` both work), `role:` and `recipe:`
derived from the run list — so recipe `search()` calls behave.

**API versions** 0, 1 and 2 are negotiated through the
`X-Ops-Server-API-Version` response header, the way erchef does it; a
version-0 client gets segment-style cookbook manifests, a version-2 client
`all_files`.

## What it is not

* **No authorisation.** Any valid key may do anything; ACLs are stored and
  served but not enforced. Doing this properly means reimplementing
  bifrost, and this is a development server.
* **A greedy dependency solver.** Newest version satisfying each constraint,
  honouring environment pins and `recipe[x@1.2.3]`. Conflicting
  constraints that a backtracking solver would resolve are reported as
  unsatisfiable.
* **Not for production.** One process, one directory, one mutex. Cookbook
  file content is held in memory.

## Layout notes

The directory is exactly what `gnife backup` writes and `gnife restore`
reads, plus two gnife-only markers: `.frozen` inside a frozen cookbook
version's directory, and `DIR/pivotal.pem` / `DIR/NAME-validator.pem` for
generated keys. TLS material (`tls.crt`, `tls.key`) is generated on first
`--tls` start and reused, so the certificate fingerprint is stable.
