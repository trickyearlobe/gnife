package main

import (
	"os"

	"github.com/trickyearlobe/gnife/cmd"
)

// Set at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	os.Exit(cmd.Execute(cmd.BuildInfo{Version: version, Commit: commit, Date: date}))
}
