package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// kindCommands builds the nouns whose verbs are entirely generic.
func kindCommands(a *app) []*cobra.Command {
	node := kindCmd(a, kinds.Node, "Nodes")
	role := kindCmd(a, kinds.Role, "Roles")
	env := kindCmd(a, kinds.Environment, "Environments")
	client := kindCmd(a, kinds.Client, "API clients")
	client.AddCommand(keyCmd(a, kinds.Client))
	user := kindCmd(a, kinds.User, "Users (server-level; needs a superuser profile)")
	user.AddCommand(keyCmd(a, kinds.User))
	group := kindCmd(a, kinds.Group, "Groups (the server's per-member 32-hex USAG groups are hidden from list)")
	group.AddCommand(groupMemberCmds(a)...)
	container := kindCmd(a, kinds.Container, "ACL containers")
	org := kindCmd(a, kinds.Organization, "Organisations (server-level; needs a superuser profile)")
	replaceCommand(org, "create", orgCreateCmd(a))
	org.AddCommand(orgMemberCmd(a))
	databag := kindCmd(a, kinds.DataBag, "Data bags")
	item := kindCmd(a, kinds.DataBagItem, "Data bag items")
	item.Use = "item"
	databag.AddCommand(item)
	pg := kindCmd(a, kinds.PolicyGroup, "Policy groups")
	pg.AddCommand(policyGroupAssignCmds(a)...)
	return []*cobra.Command{node, role, env, databag, client, user, group, container, org, pg}
}

// kindCmd builds NOUN {list,show,create,update,edit,delete,copy} for a kind.
func kindCmd(a *app, k *kinds.Kind, short string) *cobra.Command {
	c := &cobra.Command{Use: k.Name, Short: short}
	nameUse := "NAME"
	if k.Composite {
		nameUse = "NAME " + strings.ToUpper(k.SubLabel)
	}
	nameArgs := cobra.ExactArgs(1)
	maxArgs := 1
	if k.Composite {
		nameArgs = cobra.RangeArgs(1, 2)
		maxArgs = 2
	}

	c.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + k.Plural,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
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
		Use:     "show " + nameUse,
		Aliases: []string{"get"},
		Short:   "Show one " + k.Name + " as JSON",
		Args:    nameArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
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

	if k.Put != nil {
		var file string
		create := &cobra.Command{
			Use:   "create [" + nameUse + "] -f FILE",
			Short: "Create a " + k.Name + " from a JSON file (- for stdin)",
			Args:  cobra.MaximumNArgs(maxArgs),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.clientFor(k)
				if err != nil {
					return err
				}
				body, err := readJSONBody(file, k)
				if err != nil {
					return err
				}
				id, err := idFromArgsOrBody(k, args, body)
				if err != nil {
					return err
				}
				if err := k.Put(cmd.Context(), cl, id, body, false); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"created": id.String()})
			},
		}
		create.Flags().StringVarP(&file, "file", "f", "", "JSON file, or - for stdin")
		c.AddCommand(create)

		var ufile string
		update := &cobra.Command{
			Use:   "update " + nameUse + " -f FILE",
			Short: "Replace a " + k.Name + " from a JSON file (- for stdin)",
			Args:  nameArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.clientFor(k)
				if err != nil {
					return err
				}
				id, err := k.ParseID(args)
				if err != nil {
					return usageErr("%v", err)
				}
				body, err := readJSONBody(ufile, k)
				if err != nil {
					return err
				}
				if err := k.Put(cmd.Context(), cl, id, body, true); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"updated": id.String()})
			},
		}
		update.Flags().StringVarP(&ufile, "file", "f", "", "JSON file, or - for stdin")
		c.AddCommand(update)

		c.AddCommand(&cobra.Command{
			Use:   "edit " + nameUse,
			Short: "Edit a " + k.Name + " in $EDITOR",
			Args:  nameArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.clientFor(k)
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
				edited, changed, err := editInEditor(body)
				if err != nil {
					return err
				}
				if !changed {
					a.progressf("%s %s unchanged", k.Name, id)
					return nil
				}
				if err := k.Put(cmd.Context(), cl, id, edited, true); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"updated": id.String()})
			},
		})
	}

	if k.Delete != nil {
		c.AddCommand(&cobra.Command{
			Use:     "delete " + nameUse + "...",
			Aliases: []string{"rm"},
			Short:   "Delete one or more " + k.Plural,
			Args:    cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.clientFor(k)
				if err != nil {
					return err
				}
				ids, err := parseIDs(k, args)
				if err != nil {
					return usageErr("%v", err)
				}
				errs := cli.ForEach(cmd.Context(), a.concurrency(), ids, kinds.ID.String,
					func(ctx context.Context, id kinds.ID) error {
						return k.Delete(ctx, cl, id)
					})
				_ = cli.PrintJSON(a.stdout(), map[string]any{"deleted": len(ids) - len(errs)})
				return errs.Report()
			},
		})
	}

	c.AddCommand(copyCmd(a, k, nameUse))
	return c
}

// clientFor returns the right client for a kind: server-level kinds do not
// need an organisation.
func (a *app) clientFor(k *kinds.Kind) (*chef.Client, error) {
	if k.ServerLevel {
		return a.defaultClient()
	}
	return a.orgClient()
}

func idStrings(ids []kinds.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// parseIDs parses "name" or "name/sub" arguments for delete/copy.
func parseIDs(k *kinds.Kind, args []string) ([]kinds.ID, error) {
	ids := make([]kinds.ID, 0, len(args))
	for _, arg := range args {
		id, err := k.ParseID([]string{arg})
		if err != nil {
			return nil, err
		}
		if k.Composite && id.Sub == "" && k != kinds.DataBagItem {
			return nil, fmt.Errorf("%s: '%s' needs a %s (NAME/%s)", k.Name, arg, k.SubLabel, strings.ToUpper(k.SubLabel))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func readJSONBody(file string, k *kinds.Kind) (json.RawMessage, error) {
	if file == "" {
		return nil, usageErr("--file is required (use - for stdin)")
	}
	spec := "@" + file
	if file == "-" {
		spec = "-"
	}
	data, err := readBody(spec)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("%s: not valid JSON", file)
	}
	return data, nil
}

func idFromArgsOrBody(k *kinds.Kind, args []string, body json.RawMessage) (kinds.ID, error) {
	if len(args) > 0 {
		id, err := k.ParseID(args)
		if err != nil {
			return id, usageErr("%v", err)
		}
		if k.Composite && id.Sub == "" {
			if k == kinds.DataBagItem {
				id.Sub = kinds.Field(body, "id")
			}
			if id.Sub == "" {
				return id, usageErr("%s: %s required", k.Name, k.SubLabel)
			}
		}
		return id, nil
	}
	name := kinds.NameOf(body, kinds.ID{})
	if name == "" {
		return kinds.ID{}, usageErr("no name given and none found in the JSON body")
	}
	if k.Composite {
		return kinds.ID{}, usageErr("%s: NAME %s required", k.Name, strings.ToUpper(k.SubLabel))
	}
	return kinds.ID{Name: name}, nil
}

// editInEditor round-trips body through $VISUAL/$EDITOR and reports whether
// the JSON changed.
func editInEditor(body json.RawMessage) (json.RawMessage, bool, error) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		return nil, false, err
	}
	pretty.WriteByte('\n')
	tmp, err := os.CreateTemp("", "gnife-*.json")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(pretty.Bytes()); err != nil {
		tmp.Close()
		return nil, false, err
	}
	tmp.Close()

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad"
		}
	}
	parts := strings.Fields(editor)
	parts = append(parts, tmp.Name())
	ed := exec.Command(parts[0], parts[1:]...)
	ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := ed.Run(); err != nil {
		return nil, false, fmt.Errorf("editor '%s': %w", editor, err)
	}
	edited, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, false, err
	}
	if !json.Valid(edited) {
		return nil, false, fmt.Errorf("edited content is not valid JSON; nothing written")
	}
	var before, after any
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(edited, &after)
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	return edited, !bytes.Equal(b1, b2), nil
}
