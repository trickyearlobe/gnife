package cmd

import (
	"context"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

func newPolicyCmd(a *app) *cobra.Command {
	k := kinds.Policy
	c := &cobra.Command{Use: "policy", Short: "Policyfile policies and their revisions"}

	c.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List policies as NAME/REVISION",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			ids, err := k.List(cmd.Context(), cl)
			if err != nil {
				return err
			}
			return a.printList(idStrings(ids))
		},
	})
	c.AddCommand(&cobra.Command{
		Use:     "show NAME [REVISION]",
		Aliases: []string{"get"},
		Short:   "Show a revision, or the revisions of a policy",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			id, err := k.ParseID(args)
			if err != nil {
				return usageErr("%v", err)
			}
			body, err := k.Get(cmd.Context(), cl, id)
			if err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), body)
		},
	})
	var file string
	create := &cobra.Command{
		Use:   "create -f FILE",
		Short: "Create a revision from a policy lock JSON (name and revision_id come from the file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			body, err := readJSONBody(file, k)
			if err != nil {
				return err
			}
			id := kinds.ID{Name: kinds.Field(body, "name"), Sub: kinds.Field(body, "revision_id")}
			if id.Name == "" || id.Sub == "" {
				return usageErr("the policy JSON needs name and revision_id")
			}
			if err := k.Put(cmd.Context(), cl, id, body, false); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]string{"created": id.String()})
		},
	}
	create.Flags().StringVarP(&file, "file", "f", "", "policy lock JSON, or - for stdin")
	c.AddCommand(create)
	c.AddCommand(&cobra.Command{
		Use:     "delete NAME/REVISION...",
		Aliases: []string{"rm"},
		Short:   "Delete revisions (or a whole policy with just NAME)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			var ids []kinds.ID
			for _, arg := range args {
				id, err := k.ParseID([]string{arg})
				if err != nil {
					return usageErr("%v", err)
				}
				ids = append(ids, id)
			}
			errs := cli.ForEach(cmd.Context(), a.concurrency(), ids, kinds.ID.String, func(ctx context.Context, id kinds.ID) error {
				return k.Delete(ctx, cl, id)
			})
			_ = cli.PrintJSON(a.stdout(), map[string]any{"deleted": len(ids) - len(errs)})
			return errs.Report()
		},
	})
	c.AddCommand(copyCmd(a, k, "NAME/REVISION"))
	return c
}

// policyGroupAssignCmds adds assign/unassign to the generic policygroup noun.
func policyGroupAssignCmds(a *app) []*cobra.Command {
	return []*cobra.Command{
		{
			Use:   "assign GROUP POLICY REVISION",
			Short: "Assign a policy revision to a group (the revision must exist)",
			Args:  cobra.ExactArgs(3),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.orgClient()
				if err != nil {
					return err
				}
				doc, err := kinds.Policy.Get(cmd.Context(), cl, kinds.ID{Name: args[1], Sub: args[2]})
				if err != nil {
					return err
				}
				path := kinds.PolicyGroup.ObjectPath(cl, kinds.ID{Name: args[0]}) + "/policies/" + url.PathEscape(args[1])
				if err := cl.Put(cmd.Context(), path, doc, nil); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"group": args[0], "policy": args[1], "revision": args[2]})
			},
		},
		{
			Use:   "unassign GROUP POLICY",
			Short: "Remove a policy from a group",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.orgClient()
				if err != nil {
					return err
				}
				path := kinds.PolicyGroup.ObjectPath(cl, kinds.ID{Name: args[0]}) + "/policies/" + url.PathEscape(args[1])
				if err := cl.Delete(cmd.Context(), path, nil); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"group": args[0], "unassigned": args[1]})
			},
		},
	}
}
