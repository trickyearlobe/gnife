package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/config"
)

func newConfigCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Get and set gnife defaults (profile, source, dest, ...)",
		Long:  "Defaults live in ~/.gnife/config.json ($GNIFE_CONFIG). Keys:\n" + keyHelp(),
	}
	c.AddCommand(
		&cobra.Command{
			Use:   "get KEY",
			Short: "Print one value",
			Long:  "Print one value. Keys:\n" + keyHelp(),
			Args:  keyArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				if _, ok := config.Keys[args[0]]; !ok {
					return usageErr("unknown config key '%s' (known: %s)", args[0], config.KeyList())
				}
				fmt.Fprintln(a.stdout(), cfg.Get(args[0]))
				return nil
			},
		},
		&cobra.Command{
			Use:   "set KEY VALUE",
			Short: "Set a value",
			Long: "Set a value in the config file. Keys:\n" + keyHelp() + `
Examples:
  gnife config set source prod
  gnife config set dest staging
  gnife config set concurrency 32`,
			Args: keyArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				if err := cfg.Set(args[0], args[1]); err != nil {
					return usageErr("%v", err)
				}
				return cfg.Save()
			},
		},
		&cobra.Command{
			Use:   "unset KEY",
			Short: "Remove a value",
			Long:  "Remove a value from the config file. Keys:\n" + keyHelp(),
			Args:  keyArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				if err := cfg.Unset(args[0]); err != nil {
					return usageErr("%v", err)
				}
				return cfg.Save()
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "Print every value as JSON",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), cfg.Values)
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				fmt.Fprintln(a.stdout(), cfg.Path)
				return nil
			},
		},
	)
	return c
}

// keyArgs validates the argument count and, when it is wrong, shows the
// keys instead of a bare "accepts N arg(s)".
func keyArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			cmd.SetOut(os.Stderr)
			_ = cmd.Help()
			return usageErr("%s wants %s", cmd.CommandPath(), strings.TrimPrefix(cmd.Use, cmd.Name()+" "))
		}
		if _, ok := config.Keys[args[0]]; !ok {
			return usageErr("unknown config key '%s' (known: %s)", args[0], config.KeyList())
		}
		return nil
	}
}

func keyHelp() string {
	keys := make([]string, 0, len(config.Keys))
	for k := range config.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for _, k := range keys {
		s += fmt.Sprintf("  %-12s %s\n", k, config.Keys[k])
	}
	return s
}
