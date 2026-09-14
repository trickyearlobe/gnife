# gnife build, test and release. Requires only go, git and a POSIX shell.
# Versions come from git tags (see docs/build-and-release.md).

BINARY   := gnife
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
GOFLAGS  := -trimpath
export CGO_ENABLED := 0

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
SEMVER_RE := ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$

.PHONY: help build build-all test test-race lint fmt deps-check check-version install package release clean

.DEFAULT_GOAL := help

help: ## List targets (default)
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /|/' | sort -t'|' -k1,1 | awk -F'|' '{printf "  %-16s %s\n", $$1, $$2}'
	@echo "  bump-patch-push  tag the next patch version and push it (also bump-minor-push, bump-major-push; DRY_RUN=1 to preview)"

build: ## Build ./gnife for this machine
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) .

build-all: ## Cross-compile every platform into dist/
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  out=dist/$(BINARY)-$$os-$$arch$$ext; echo "  $$out"; \
	  GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$out . || exit 1; \
	done

test: ## Run the tests
	go test ./...

test-race: ## Run the tests with the race detector (needs a C toolchain)
	CGO_ENABLED=1 go test -race ./...

lint: ## go vet + gofmt check
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

fmt: ## gofmt the tree
	gofmt -w .

deps-check: ## Fail if a module compiled into the binary is not in deps-allowlist.txt
	@go mod verify
	@bad=""; for m in $$(GOOS=windows go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./... | sort -u); do \
	  grep -qxF "$$m" deps-allowlist.txt || bad="$$bad $$m"; \
	done; \
	if [ -n "$$bad" ]; then echo "modules not in deps-allowlist.txt:$$bad"; exit 1; fi

check-version: ## Fail unless VERSION is a semver tag that HEAD is exactly at
	@echo "$(VERSION)" | grep -Eq '$(SEMVER_RE)' \
	  || { echo "VERSION '$(VERSION)' is not a semver tag (vX.Y.Z[-pre]); tag the commit first"; exit 1; }
	@case "$(VERSION)" in *-dirty) echo "VERSION '$(VERSION)' is a dirty build"; exit 1;; esac
	@t=$$(git describe --tags --exact-match HEAD 2>/dev/null) || { echo "HEAD is not tagged; VERSION '$(VERSION)' cannot be released"; exit 1; }; \
	[ "$$t" = "$(VERSION)" ] || { echo "VERSION '$(VERSION)' is not the tag at HEAD ($$t)"; exit 1; }

install: ## go install
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' .

package: build-all ## Package dist/ as tar.gz (unix) / zip (windows) with SHA256SUMS
	@cd dist && rm -f SHA256SUMS *.tar.gz *.zip && for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; base=$(BINARY)-$$os-$$arch; \
	  if [ $$os = windows ]; then \
	    cp $$base.exe $(BINARY).exe && zip -q $$base.zip $(BINARY).exe && rm $(BINARY).exe; \
	  else \
	    cp $$base $(BINARY) && tar czf $$base.tar.gz $(BINARY) && rm $(BINARY); \
	  fi; \
	done && shasum -a 256 *.tar.gz *.zip > SHA256SUMS && cat SHA256SUMS

release: check-version package ## check-version, then package

clean: ## Remove built binaries and dist/
	rm -rf $(BINARY) $(BINARY).exe dist

# bump-patch-push | bump-minor-push | bump-major-push
# Tag the next semver version on HEAD and push the tag; CI builds the release.
# DRY_RUN=1 prints what would happen.
bump-%-push:
	@set -e; kind=$*; \
	case $$kind in patch|minor|major) ;; *) echo "unknown bump type '$$kind' (patch|minor|major)"; exit 2;; esac; \
	[ -z "$$(git status --porcelain)" ] || { echo "working tree is dirty; commit or stash first"; exit 1; }; \
	git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1 || { echo "current branch has no upstream"; exit 1; }; \
	if t=$$(git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null); then echo "HEAD is already tagged: $$t"; exit 1; fi; \
	cur=$$(git tag --list 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | sort -V | tail -1); cur=$${cur:-v0.0.0}; \
	v=$${cur#v}; major=$${v%%.*}; rest=$${v#*.}; minor=$${rest%%.*}; patch=$${rest#*.}; \
	case $$kind in \
	  patch) patch=$$((patch+1));; \
	  minor) minor=$$((minor+1)); patch=0;; \
	  major) major=$$((major+1)); minor=0; patch=0;; \
	esac; \
	new=v$$major.$$minor.$$patch; echo "$$cur -> $$new"; \
	if [ -n "$(DRY_RUN)" ]; then echo "DRY_RUN: git tag -a $$new -m $$new && git push origin $$new"; exit 0; fi; \
	git tag -a $$new -m $$new && git push origin $$new
