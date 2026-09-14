// Package cmd wires the cobra command tree. Commands parse flags and call
// into internal packages; they do not talk HTTP themselves.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
)

// BuildInfo is injected by main from -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Execute runs the CLI and returns the process exit code.
func Execute(build BuildInfo) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{build: build}
	root := newRootCmd(a)
	root.SetContext(ctx)
	err := root.Execute()
	if err == nil {
		return cli.ExitOK
	}
	if isCobraUsageError(err) {
		// cobra's parse errors: exit 2, one line, quoted like the rest of gnife.
		msg := strings.ReplaceAll(err.Error(), `"`, "'")
		if i := strings.Index(msg, "\n\nDid you mean this?\n"); i >= 0 {
			suggestions := strings.Fields(msg[i+len("\n\nDid you mean this?\n"):])
			msg = msg[:i] + " (did you mean: " + strings.Join(suggestions, ", ") + "?)"
		} else if strings.HasPrefix(msg, "unknown command") {
			msg += " (run 'gnife help')"
		}
		err = cli.Usage(errors.New(msg))
	}
	code := cli.ExitCode(err)
	var ee *cli.ExitError
	if errors.As(err, &ee) && ee.Err == nil {
		return code
	}
	if chef.IsNotPermitted(err) && len(a.used) > 0 {
		// Say who asked: the usual cause is the wrong profile.
		err = fmt.Errorf("%w (as profile %s; use -p or 'gnife config set profile')", err, strings.Join(a.used, ", "))
	}
	// Errors are JSON on stderr, like every other output.
	enc := json.NewEncoder(os.Stderr)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"error": err.Error()})
	return code
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "gnife",
		Short: "Fast CLI for one or many Chef Infra Server organisations",
		Long: `gnife manipulates every Chef Infra Server object type, backs up and restores
whole organisations, and copies objects (with their dependencies) between
organisations. Credentials come from ~/.chef/credentials; defaults such as
the source and destination profile are set with "gnife config".

Output is JSON on stdout; warnings, progress and errors are on stderr.
Exit codes: 0 ok, 1 error, 2 usage, 3 partial (some items failed).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&a.flagProfile, "profile", "p", "", "credentials profile (default: $CHEF_PROFILE, config, ~/.chef/context, \"default\")")
	pf.StringVar(&a.flagConfig, "config", "", "gnife config file (default $GNIFE_CONFIG or ~/.gnife/config.json)")
	pf.StringVar(&a.flagCredentials, "credentials", "", "Chef credentials file (default ~/.chef/credentials)")
	pf.IntVar(&a.flagConcurrency, "concurrency", 0, "workers for bulk operations (default from config, else 16)")
	pf.StringVarP(&a.flagOutput, "output", "o", "", "output format for list commands: json or names")
	pf.BoolVarP(&a.flagQuiet, "quiet", "q", false, "suppress progress messages on stderr")
	pf.BoolVar(&a.flagDebug, "debug", false, "trace every HTTP request on stderr (never prints credentials)")

	root.AddCommand(
		newVersionCmd(a),
		newConfigCmd(a),
		newProfileCmd(a),
		newRawCmd(a),
		newStatusCmd(a),
		newSearchCmd(a),
	)
	for _, c := range kindCommands(a) {
		root.AddCommand(c)
	}
	root.AddCommand(
		newCookbookCmd(a),
		newArtifactCmd(a),
		newPolicyCmd(a),
		newACLCmd(a),
		newBackupCmd(a),
		newRestoreCmd(a),
		newCloneCmd(a),
		newServeCmd(a),
	)
	return root
}

// cobraUsagePrefixes are the messages cobra/pflag produce before a command
// runs: unknown commands and flags, wrong argument counts.
var cobraUsagePrefixes = []string{
	"unknown command", "unknown flag", "unknown shorthand flag", "flag needs an argument",
	"invalid argument", "accepts ", "requires at least", "requires exactly", "required flag(s)",
	"bad flag syntax",
}

func isCobraUsageError(err error) bool {
	var ee *cli.ExitError
	if errors.As(err, &ee) {
		return false
	}
	msg := err.Error()
	for _, p := range cobraUsagePrefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// usageErr marks an error as a usage problem (exit 2).
func usageErr(format string, args ...any) error {
	return cli.Usage(fmt.Errorf(format, args...))
}

// exitCode is cli.ExitCode, exposed for tests in this package.
func exitCode(err error) int { return cli.ExitCode(err) }
