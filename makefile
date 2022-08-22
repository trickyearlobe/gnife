# Habitat provides a build timestamp in $pkg_prefix.
# Make sure we have $pkg_prefix if we build outside Habitat
pkg_prefix ?= $(shell date +"%Y%m%d%H%M%S")
git_commit ?= $(shell git rev-parse HEAD)
git_user ?= $(shell git config user.email)
git_branch ?= $(shell git branch --show-current)
LDFLAGS := '-X main.Build=$(pkg_prefix) -X main.GitCommit=$(git_commit) -X main.BuiltByName=$(git_user) -X main.GitBranch=$(git_branch)'

build:
	@go get

	@# Darwin/OSX builds
	GOOS=darwin GOARCH=amd64 go build -ldflags $(LDFLAGS) -o binaries/darwin/amd64/gnife main.go
	GOOS=darwin GOARCH=arm64 go build -ldflags $(LDFLAGS) -o binaries/darwin/arm64/gnife main.go

	@# Linux builds
	GOOS=linux GOARCH=amd64 go build -ldflags $(LDFLAGS) -o binaries/linux/amd64/gnife main.go
	GOOS=linux GOARCH=arm64 go build -ldflags $(LDFLAGS) -o binaries/linux/arm64/gnife main.go

	@# Windows builds
	GOOS=windows GOARCH=amd64 go build -ldflags $(LDFLAGS) -o binaries/windows/amd64/gnife.exe main.go
	GOOS=windows GOARCH=arm64 go build -ldflags $(LDFLAGS) -o binaries/windows/arm64/gnife.exe main.go

# Installs our project: copies binaries
install:
	go install -ldflags $(LDFLAGS)

# Cleans our project: deletes binaries
clean:
	@if [ -f gnife ] ; then rm gnife ; fi
	@if [ -d binaries ] ; then rm -rf binaries; fi

test:
	@go test github.com/trickyearlobe/gnife/components

all: clean test install

.PHONY: clean install test