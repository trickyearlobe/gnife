package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/backup"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/credentials"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

func kindNamesHelp() string {
	var names []string
	for _, k := range append(append([]*kinds.Kind{}, backup.JSONKinds...), backup.FilesKinds...) {
		if k == kinds.DataBagItem {
			names = append(names, kinds.DataBag.Name) // items travel with their bag
			continue
		}
		names = append(names, k.Name)
	}
	return strings.Join(names, ", ")
}

func newBackupCmd(a *app) *cobra.Command {
	var (
		dir       string
		only      []string
		skip      []string
		skipUsers bool
		skipACLs  bool
		archive   bool
		inUse     bool
	)
	c := &cobra.Command{
		Use:   "backup --dir DIR",
		Short: "Back up the whole organisation to DIR (knife-ec-backup layout)",
		Long: `Write every object of the profile's organisation under DIR/organizations/ORG
in the layout knife ec backup uses, plus DIR/users when the profile may read
them. Kinds for --only/--skip: ` + kindNamesHelp() + `.

--in-use backs up only what a node can reach: its environment, roles, the
cookbook versions the depsolver picks for its run list (or its policy
revision, artifacts and policy group), plus every container, group, client
and data bag. Unreferenced cookbook versions, roles, environments, policies
and artifacts are left out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			res, err := backup.Backup(cmd.Context(), cl, dir, backup.Options{
				Workers: a.concurrency(), SkipUsers: skipUsers, SkipACLs: skipACLs,
				Only: only, Skip: skip, Version: a.build.Version, InUse: inUse,
				Progress: a.progressf, Warn: cli.Warnf,
			})
			if err != nil {
				return err
			}
			if archive {
				file := strings.TrimSuffix(filepath.Clean(dir), string(filepath.Separator)) + ".tar.gz"
				if err := backup.Archive(dir, file); err != nil {
					return err
				}
				a.progressf("wrote %s", file)
			}
			if err := cli.PrintJSON(a.stdout(), res); err != nil {
				return err
			}
			return res.Failed.Partial()
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "backup directory (created if missing)")
	_ = c.MarkFlagRequired("dir")
	c.Flags().StringSliceVar(&only, "only", nil, "only these kinds (comma separated)")
	c.Flags().StringSliceVar(&skip, "skip", nil, "skip these kinds (comma separated)")
	c.Flags().BoolVar(&skipUsers, "skip-users", false, "do not back up server-level users (they are skipped anyway when the profile may not read them)")
	c.Flags().BoolVar(&skipACLs, "skip-acls", false, "do not back up ACLs")
	c.Flags().BoolVar(&archive, "archive", false, "also write DIR.tar.gz")
	c.Flags().BoolVar(&inUse, "in-use", false, "only objects a node can reach (plus containers, groups, clients, data bags)")
	return c
}

type restoreFlags struct {
	dir       string
	org       string
	only      []string
	skip      []string
	skipUsers bool
	skipACLs  bool
	createOrg bool
	keyFile   string
	overwrite bool
	force     bool
	purge     bool
	dryRun    bool
}

func (f *restoreFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.org, "org", "", "organisation directory to restore from when the backup holds several")
	c.Flags().StringSliceVar(&f.only, "only", nil, "only these kinds (comma separated)")
	c.Flags().StringSliceVar(&f.skip, "skip", nil, "skip these kinds (comma separated)")
	c.Flags().BoolVar(&f.skipUsers, "skip-users", false, "do not restore server-level users or organisation membership")
	c.Flags().BoolVar(&f.skipACLs, "skip-acls", false, "do not restore ACLs")
	c.Flags().BoolVar(&f.createOrg, "create-org", false, "create the destination organisation if missing (superuser)")
	c.Flags().StringVar(&f.keyFile, "validator-keyfile", "", "with --create-org: where to save the new validator key (default ~/.chef/ORG-validator.pem)")
	c.Flags().BoolVar(&f.overwrite, "overwrite", false, "replace objects that already exist")
	c.Flags().BoolVar(&f.force, "force", false, "with --overwrite: replace frozen cookbook versions too")
	c.Flags().BoolVar(&f.purge, "purge", false, "delete destination objects that are not in the backup")
	c.Flags().BoolVar(&f.dryRun, "dry-run", false, "print what would be restored (and purged) and change nothing")
}

func (f *restoreFlags) options(a *app, org string) backup.RestoreOptions {
	keyFile := f.keyFile
	if keyFile == "" && f.createOrg {
		keyFile = filepath.Join(credentials.ChefDir(), org+"-validator.pem")
	}
	return backup.RestoreOptions{
		Options: backup.Options{
			Workers: a.concurrency(), SkipUsers: f.skipUsers, SkipACLs: f.skipACLs,
			Only: f.only, Skip: f.skip, Version: a.build.Version,
			Progress: a.progressf, Warn: cli.Warnf,
		},
		OrgDir: f.org, CreateOrg: f.createOrg, ValidatorKeyFile: keyFile, Overwrite: f.overwrite,
		Force: f.force, Purge: f.purge, DryRun: f.dryRun,
	}
}

func newRestoreCmd(a *app) *cobra.Command {
	var f restoreFlags
	c := &cobra.Command{
		Use:   "restore --dir DIR",
		Short: "Restore a backup into the profile's organisation",
		Long: `Restore DIR (a backup directory or .tar.gz) into the organisation named in
the profile's chef_server_url. The directory under DIR/organizations is only
a source: rename it freely, or pick one with --org. Existing objects are
skipped unless --overwrite; --purge removes objects the backup lacks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			root := f.dir
			if backup.IsArchive(root) {
				tmp, err := backup.Extract(root)
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				root = tmp
			}
			if !backup.Exists(root) {
				return fmt.Errorf("%s: not a backup directory (no organizations/)", f.dir)
			}
			res, err := backup.Restore(cmd.Context(), cl, root, f.options(a, cl.Org()))
			if err != nil {
				return err
			}
			if err := cli.PrintJSON(a.stdout(), res); err != nil {
				return err
			}
			return res.Err()
		},
	}
	c.Flags().StringVar(&f.dir, "dir", "", "backup directory or .tar.gz")
	_ = c.MarkFlagRequired("dir")
	f.bind(c)
	return c
}

func newCloneCmd(a *app) *cobra.Command {
	var (
		f     restoreFlags
		from  string
		to    string
		inUse bool
	)
	c := &cobra.Command{
		Use:   "clone --from A --to B",
		Short: "Copy a whole organisation: backup to a temporary directory, then restore",
		Long: `Copy every object of the --from organisation into the --to organisation.
Existing objects are skipped unless --overwrite; --purge removes what the
source lacks; --dry-run lists the plan.

--in-use copies only what a node can reach (its environment, roles, the
cookbook versions its run list resolves to, or its policy revision,
artifacts and policy group) plus every container, group, client and data
bag, straight from server to server. --purge is not available with it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			src, dst, err := a.sourceDest(from, to)
			if err != nil {
				return err
			}
			if inUse {
				return a.cloneInUse(cmd, src, dst, f)
			}
			tmp, err := os.MkdirTemp("", "gnife-clone-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			a.progressf("backing up %s to %s", src.Org(), tmp)
			bres, err := backup.Backup(cmd.Context(), src, tmp, backup.Options{
				Workers: a.concurrency(), SkipUsers: f.skipUsers, SkipACLs: f.skipACLs,
				Only: f.only, Skip: f.skip, Version: a.build.Version,
				Progress: a.progressf, Warn: cli.Warnf,
			})
			if err != nil {
				return err
			}
			opts := f.options(a, dst.Org())
			opts.OrgDir = src.Org()
			res, err := backup.Restore(cmd.Context(), dst, tmp, opts)
			if err != nil {
				return err
			}
			res.Failed = append(res.Failed, bres.Failed...)
			if err := cli.PrintJSON(a.stdout(), res); err != nil {
				return err
			}
			return res.Err()
		},
	}
	c.Flags().StringVar(&from, "from", "", "source profile (default: config source)")
	c.Flags().StringVar(&to, "to", "", "destination profile (default: config dest)")
	c.Flags().BoolVar(&inUse, "in-use", false, "only objects a node can reach (plus containers, groups, clients, data bags)")
	f.bind(c)
	_ = c.Flags().MarkHidden("org")
	return c
}

// cloneInUse plans the in-use closure on the source and executes it against
// the destination without touching disk.
func (a *app) cloneInUse(cmd *cobra.Command, src, dst *chef.Client, f restoreFlags) error {
	if f.purge {
		return usageErr("--purge cannot be combined with --in-use")
	}
	if f.createOrg {
		return usageErr("--create-org is only available for a full clone")
	}
	opts := f.options(a, dst.Org())
	a.progressf("%s: planning what is in use", src.Org())
	plan, err := transfer.InUsePlan(cmd.Context(), src, !f.skipACLs)
	if err != nil {
		return err
	}
	plan = plan.Filter(opts.Wants)
	if f.dryRun {
		return cli.PrintJSON(a.stdout(), plan.Summary())
	}
	a.progressf("copying %d objects from %s/%s to %s/%s", len(plan.Items), src.ServerRoot(), src.Org(), dst.ServerRoot(), dst.Org())
	res := transfer.Execute(cmd.Context(), transfer.NewServerSource(src), dst, plan, transfer.ExecOptions{
		Overwrite: f.overwrite, Force: f.force, Workers: a.concurrency(),
		Progress: a.progressf, Warn: cli.Warnf,
	})
	if err := cli.PrintJSON(a.stdout(), res); err != nil {
		return err
	}
	return res.Err()
}
