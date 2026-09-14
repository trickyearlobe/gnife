package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
)

func newVersionCmd(a *app) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the version (from the git tag it was built at)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return cli.PrintJSON(a.stdout(), map[string]string{
					"version": a.build.Version,
					"commit":  a.build.Commit,
					"date":    a.build.Date,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				})
			}
			s := "gnife " + a.build.Version
			if a.build.Commit != "" {
				s += " (" + a.build.Commit
				if a.build.Date != "" {
					s += ", " + a.build.Date
				}
				s += ")"
			}
			fmt.Fprintf(a.stdout(), "%s %s %s/%s\n", s, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return c
}
