package cmd

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
)

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server status (/_status)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.defaultClient()
			if err != nil {
				return err
			}
			var out json.RawMessage
			if err := c.Get(cmd.Context(), "/_status", &out); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), out)
		},
	}
}
