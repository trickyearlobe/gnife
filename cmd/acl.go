package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

func aclKindNames() string {
	names := []string{"organization"}
	for _, k := range acl.Kinds {
		names = append(names, k.Name)
	}
	return strings.Join(names, ", ")
}

// aclTarget resolves KIND NAME to an ACL path. KIND "organization" needs no name.
func aclTarget(cl *chef.Client, args []string) (*kinds.Kind, string, string, error) {
	if args[0] == "organization" || args[0] == "org" {
		return nil, "", acl.OrgPath(cl), nil
	}
	k, ok := kinds.Get(args[0])
	if !ok {
		return nil, "", "", usageErr("unknown kind '%s' (one of: %s)", args[0], aclKindNames())
	}
	if len(args) < 2 {
		return nil, "", "", usageErr("%s needs a NAME", k.Name)
	}
	return k, args[1], acl.Path(cl, k, args[1]), nil
}

func newACLCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "acl",
		Short: "Object ACLs",
		Long:  "Show, replace and copy the ACL of an object. KIND is one of: " + aclKindNames() + ".",
	}
	c.AddCommand(&cobra.Command{
		Use:     "show KIND [NAME]",
		Aliases: []string{"get"},
		Short:   "Show an ACL",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			_, _, path, err := aclTarget(cl, args)
			if err != nil {
				return err
			}
			got, err := acl.Get(cmd.Context(), cl, path)
			if err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), got)
		},
	})
	var file string
	update := &cobra.Command{
		Use:   "update KIND [NAME] -f FILE",
		Short: "Replace an ACL from JSON (the output shape of acl show)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			_, _, path, err := aclTarget(cl, args)
			if err != nil {
				return err
			}
			body, err := readJSONBody(file, kinds.Node)
			if err != nil {
				return err
			}
			var a1 acl.ACL
			if err := json.Unmarshal(body, &a1); err != nil {
				return fmt.Errorf("parsing ACL: %w", err)
			}
			if err := acl.Put(cmd.Context(), cl, path, a1); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]string{"updated": strings.Join(args, " ")})
		},
	}
	update.Flags().StringVarP(&file, "file", "f", "", "ACL JSON, or - for stdin")
	c.AddCommand(update)

	var f copyFlags
	cp := &cobra.Command{
		Use:   "copy KIND [NAME]",
		Short: "Copy an object's ACL to the same object on another organisation",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, dst, err := a.sourceDest(f.from, f.to)
			if err != nil {
				return err
			}
			k, name, _, err := aclTarget(src, args)
			if err != nil {
				return err
			}
			plan := &transfer.Plan{ACLs: []transfer.ACLItem{{Kind: k, Name: name}}}
			if f.dryRun {
				return cli.PrintJSON(a.stdout(), plan.Summary())
			}
			res := transfer.Execute(cmd.Context(), transfer.NewServerSource(src), dst, plan, transfer.ExecOptions{
				Workers: a.concurrency(), Progress: a.progressf, Warn: cli.Warnf,
			})
			if err := cli.PrintJSON(a.stdout(), res); err != nil {
				return err
			}
			return res.Err()
		},
	}
	cp.Flags().StringVar(&f.from, "from", "", "source profile (default: config source)")
	cp.Flags().StringVar(&f.to, "to", "", "destination profile (default: config dest)")
	cp.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	c.AddCommand(cp)
	return c
}
