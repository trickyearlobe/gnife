package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/deps"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

func newCookbookCmd(a *app) *cobra.Command {
	c := filesKindCmd(a, kinds.Cookbook, "Cookbooks")
	freeze := &cobra.Command{
		Use:   "freeze NAME VERSION",
		Short: "Freeze a cookbook version",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			id := kinds.ID{Name: args[0], Sub: args[1]}
			m, err := cookbook.Fetch(cmd.Context(), cl, kinds.Cookbook, id)
			if err != nil {
				return err
			}
			if err := cl.Put(cmd.Context(), kinds.Cookbook.ObjectPath(cl, id), m.Body(true), nil); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]any{"frozen": id.String()})
		},
	}
	c.AddCommand(freeze)
	return c
}

func newArtifactCmd(a *app) *cobra.Command {
	c := filesKindCmd(a, kinds.Artifact, "Cookbook artifacts (Policyfile cookbooks)")
	return c
}

// filesKindCmd builds list/show/download/upload/delete/copy for cookbooks and artifacts.
func filesKindCmd(a *app, k *kinds.Kind, short string) *cobra.Command {
	sub := "VERSION"
	if k == kinds.Artifact {
		sub = "IDENTIFIER"
	}
	c := &cobra.Command{Use: k.Name, Short: short}

	var allVersions bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + k.Plural + " (latest version each, or --all-versions)",
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
			if allVersions || k == kinds.Artifact {
				return a.printList(idStrings(ids))
			}
			byName := map[string][]string{}
			for _, id := range ids {
				byName[id.Name] = append(byName[id.Name], id.Sub)
			}
			var out []string
			for name, versions := range byName {
				latest, _ := deps.Latest(versions)
				out = append(out, name+"/"+latest)
			}
			sort.Strings(out)
			return a.printList(out)
		},
	}
	list.Flags().BoolVar(&allVersions, "all-versions", false, "list every version")
	c.AddCommand(list)

	c.AddCommand(&cobra.Command{
		Use:     "show NAME [" + sub + "]",
		Aliases: []string{"get"},
		Short:   "Show the manifest of one version, or the versions of a name",
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

	var dir string
	download := &cobra.Command{
		Use:   "download NAME " + sub + " [--dir DIR]",
		Short: "Download a version into DIR/NAME-" + sub,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			id := kinds.ID{Name: args[0], Sub: args[1]}
			m, err := cookbook.Fetch(cmd.Context(), cl, k, id)
			if err != nil {
				return err
			}
			target := filepath.Join(dir, cookbook.DirName(id))
			if err := cookbook.Download(cmd.Context(), cl, m, target, a.concurrency()); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]any{"downloaded": id.String(), "dir": target, "files": len(m.Files)})
		},
	}
	download.Flags().StringVar(&dir, "dir", ".", "parent directory")
	c.AddCommand(download)

	var (
		freeze bool
		force  bool
		upName string
		upSub  string
	)
	upload := &cobra.Command{
		Use:   "upload DIR",
		Short: "Upload a cookbook directory (needs metadata.json)",
		Long: `Upload the cookbook in DIR. The name and version come from metadata.json
unless --name/--` + map[bool]string{true: "identifier", false: "version"}[k == kinds.Artifact] + ` override them. metadata.rb is not evaluated;
run "knife cookbook metadata" or "chef" first if metadata.json is missing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			id := kinds.ID{Name: upName, Sub: upSub}
			if id.Name == "" {
				meta, err := os.ReadFile(filepath.Join(args[0], "metadata.json"))
				if err != nil {
					return fmt.Errorf("reading metadata.json: %w", err)
				}
				id.Name = kinds.Field(json.RawMessage(meta), "name")
				if id.Name == "" {
					return usageErr("metadata.json has no name; pass --name")
				}
			}
			m, err := cookbook.UploadDir(cmd.Context(), cl, k, id, args[0], cookbook.UploadOptions{
				Force: force, Freeze: freeze, Workers: a.concurrency(),
			})
			if err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]any{"uploaded": m.ID.String(), "files": len(m.Files), "frozen": freeze})
		},
	}
	upload.Flags().BoolVar(&freeze, "freeze", false, "freeze the version after upload")
	upload.Flags().BoolVar(&force, "force", false, "replace a frozen version")
	upload.Flags().StringVar(&upName, "name", "", "override the cookbook name")
	if k == kinds.Artifact {
		upload.Flags().StringVar(&upSub, "identifier", "", "artifact identifier (required)")
		_ = upload.MarkFlagRequired("identifier")
	} else {
		upload.Flags().StringVar(&upSub, "version", "", "override the version")
	}
	c.AddCommand(upload)

	c.AddCommand(&cobra.Command{
		Use:     "delete NAME " + sub + "...",
		Aliases: []string{"rm"},
		Short:   "Delete versions (NAME " + sub + ", or NAME/" + sub + " pairs)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			var ids []kinds.ID
			if len(args) == 2 && !containsSlash(args[0]) {
				ids = []kinds.ID{{Name: args[0], Sub: args[1]}}
			} else {
				ids, err = parseIDs(k, args)
				if err != nil {
					return usageErr("%v", err)
				}
			}
			errs := cli.ForEach(cmd.Context(), a.concurrency(), ids, kinds.ID.String, func(ctx0 context.Context, id kinds.ID) error {
				return k.Delete(ctx0, cl, id)
			})
			_ = cli.PrintJSON(a.stdout(), map[string]any{"deleted": len(ids) - len(errs)})
			return errs.Report()
		},
	})

	c.AddCommand(copyCmd(a, k, "NAME[/"+sub+"]"))
	return c
}

func containsSlash(s string) bool {
	for _, r := range s {
		if r == '/' {
			return true
		}
	}
	return false
}
