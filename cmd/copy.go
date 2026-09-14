package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

// copyFlags are shared by every NOUN copy command.
type copyFlags struct {
	from, to    string
	deps        bool
	acl         bool
	overwrite   bool
	force       bool
	dryRun      bool
	all         bool
	environment string
}

func (f *copyFlags) bind(c *cobra.Command, k *kinds.Kind) {
	c.Flags().StringVar(&f.from, "from", "", "source profile (default: config source)")
	c.Flags().StringVar(&f.to, "to", "", "destination profile (default: config dest)")
	c.Flags().BoolVar(&f.deps, "deps", false, "also copy dependencies (roles, cookbooks, environments, policies, artifacts, member groups)")
	c.Flags().BoolVar(&f.acl, "acl", false, "also copy ACLs (actors missing on the destination are dropped)")
	c.Flags().BoolVar(&f.overwrite, "overwrite", false, "replace objects that already exist on the destination")
	c.Flags().BoolVar(&f.force, "force", false, "with --overwrite: replace frozen cookbook versions too")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the plan and change nothing")
	c.Flags().BoolVar(&f.all, "all", false, "copy every "+k.Name+" on the source")
	if k == kinds.Role || k == kinds.Node {
		c.Flags().StringVar(&f.environment, "environment", "", "environment to resolve role cookbooks in (default _default)")
	}
}

func copyCmd(a *app, k *kinds.Kind, nameUse string) *cobra.Command {
	var f copyFlags
	c := &cobra.Command{
		Use:   "copy [" + nameUse + "...]",
		Short: "Copy " + k.Plural + " to another organisation",
		Long: fmt.Sprintf(`Copy %s from the --from organisation to the --to organisation. Names are
NAME or NAME/%s for composite objects. --deps pulls in what the object needs;
--dry-run shows the resulting plan as JSON.`, k.Plural, strings.ToUpper(k.SubLabel)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !f.all {
				return usageErr("give one or more names, or --all")
			}
			if k.ServerLevel {
				return usageErr("%s are server-level objects and are not copied between organisations", k.Plural)
			}
			src, dst, err := a.sourceDest(f.from, f.to)
			if err != nil {
				return err
			}
			var ids []kinds.ID
			if f.all {
				ids, err = k.List(cmd.Context(), src)
				if err != nil {
					return err
				}
			} else {
				ids, err = parseIDsLoose(k, args)
				if err != nil {
					return usageErr("%v", err)
				}
			}
			items := make([]transfer.Item, 0, len(ids))
			for _, id := range ids {
				items = append(items, transfer.Item{Kind: k, ID: id, Reason: "requested"})
			}
			return a.runCopy(cmd, src, dst, items, f)
		},
	}
	f.bind(c, k)
	return c
}

// parseIDsLoose is parseIDs but lets files kinds omit the version (latest).
func parseIDsLoose(k *kinds.Kind, args []string) ([]kinds.ID, error) {
	if k.Files || k == kinds.DataBagItem {
		ids := make([]kinds.ID, 0, len(args))
		for _, arg := range args {
			id, err := k.ParseID([]string{arg})
			if err != nil {
				return nil, err
			}
			if k == kinds.DataBagItem && id.Sub == "" {
				return nil, fmt.Errorf("'%s': BAG/ITEM required", arg)
			}
			ids = append(ids, id)
		}
		return ids, nil
	}
	return parseIDs(k, args)
}

func (a *app) runCopy(cmd *cobra.Command, s, d *chef.Client, items []transfer.Item, f copyFlags) error {
	plan, err := transfer.BuildPlan(cmd.Context(), s, items, transfer.PlanOptions{
		Deps: f.deps, ACL: f.acl, Environment: f.environment,
	})
	if err != nil {
		return err
	}
	if f.dryRun {
		return cli.PrintJSON(a.stdout(), plan.Summary())
	}
	a.progressf("copying %d objects from %s/%s to %s/%s", len(plan.Items), s.ServerRoot(), s.Org(), d.ServerRoot(), d.Org())
	res := transfer.Execute(cmd.Context(), transfer.NewServerSource(s), d, plan, transfer.ExecOptions{
		Overwrite: f.overwrite, Force: f.force, Workers: a.concurrency(),
		Progress: a.progressf, Warn: cli.Warnf,
	})
	if err := cli.PrintJSON(a.stdout(), res); err != nil {
		return err
	}
	return res.Err()
}
