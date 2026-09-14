# Versioning, build and release

## Versioning

Versions come from git tags and nothing else. Tags are semver with a `v`
prefix: `v1.4.0`, `v2.0.0-rc.1`. There is no version constant in the source.

* `VERSION ?= $(shell git describe --tags --always --dirty)` in the Makefile.
  On a tagged commit that is the tag itself (`v1.4.0`); on any other commit
  it is `v1.4.0-3-g1a2b3c4[-dirty]`, so a local build always says what it is.
* `make release` and CI refuse to release unless `VERSION` is a semver tag
  (`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`), is not `-dirty`, and is
  exactly the tag at HEAD (`git describe --tags --exact-match`). A regex
  alone cannot tell `v1.2.3-rc.1` from git describe's `v1.2.3-4-gabc1234`,
  so the exact-match check is the one that matters.
* Embedded via `-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)
  -X main.date=$(DATE)"`; `gnife version` prints all three (and `-o json`).
  A build with no ldflags reports `dev`.
* `X-Chef-Version`/`User-Agent` carry the same string.

## Makefile

```
make build        CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X ..." (see Versioning)
make build-all    six binaries into dist/: linux|darwin|windows × amd64|arm64
make test         go test ./...
make test-race    CGO_ENABLED=1 go test -race ./...   (needs a C toolchain)
make lint         go vet ./... ; gofmt -l check
make deps-check   modules compiled into the binary vs deps-allowlist.txt
make check-version  fail unless VERSION is the semver tag at HEAD
make install      go install
make package      build-all + SHA256SUMS + tar.gz (unix) / zip (windows) per target
make release      check-version + package
make clean

make bump-patch-push   tag the next patch version and push the tag   v1.4.2 -> v1.4.3
make bump-minor-push   tag the next minor version and push the tag   v1.4.2 -> v1.5.0
make bump-major-push   tag the next major version and push the tag   v1.4.2 -> v2.0.0
```

### Bump targets

Releasing is `make bump-patch-push`; nothing else. Tagging and pushing are
one operation — there is no tag-only target, because a local tag that is
never pushed is just a trap for the next `git describe`. The targets are
plain shell in the Makefile (git + sed, no external tools), one pattern rule
`bump-%-push`:

1. Refuse if the working tree is dirty (`git status --porcelain`), if HEAD
   is already tagged with a semver tag, or if the current branch has no
   upstream.
2. Find the latest release tag: `git tag --list 'v[0-9]*' | sort -V | tail -1`
   (`sort -V` is in coreutils and BSD sort on macOS). No tag yet → `v0.0.0`
   is the base, so the first `bump-minor-push` produces `v0.1.0`.
   Pre-release tags (`v2.0.0-rc.1`) are excluded from the base; those are
   made by hand with `git tag` and `git push origin <tag>`.
3. Compute the next version, print `v1.4.2 -> v1.4.3`, create an
   **annotated** tag on HEAD (`git tag -a v1.4.3 -m v1.4.3`) and
   `git push origin v1.4.3` — the tag only, never the branch (the commits
   the tag references go with it).

`DRY_RUN=1 make bump-minor-push` prints what would happen and touches
nothing.

## GitHub Actions

`.github/workflows/release.yml` — triggered **only** by tag pushes:

```yaml
on:
  push:
    tags: ['v*.*.*']
```

No branch or pull-request builds. Day-to-day verification is `make lint test`
locally; the pipeline exists to produce release artifacts. A tag whose tests
fail produces no release: delete the tag, fix, re-tag.

* `test` job — matrix `ubuntu-latest`, `macos-latest`, `windows-latest`:
  `actions/checkout` (with `fetch-depth: 0` and `fetch-tags: true` so
  `git describe` sees the tag), `actions/setup-go` (both SHA-pinned, Go
  version from `go.mod`), then `make deps-check lint test-race`. The Windows
  runner has no POSIX shell for make and no guaranteed C toolchain for
  `-race`, so it runs the equivalent `go` commands directly, without `-race`.
* `release` job — `needs: test`, one `ubuntu-latest` runner: `make
  check-version` (tag must be semver; also asserts `${{ github.ref_name }}`
  equals `git describe --tags --exact-match`), `make release` (Go
  cross-compiles all six targets; no per-OS build runners), then
  `gh release create "$GITHUB_REF_NAME" dist/*` with the built-in
  `GITHUB_TOKEN` — no third-party release action. A pre-release semver tag
  (`v2.0.0-rc.1`) is published with `--prerelease`.
