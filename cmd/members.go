package cmd

import (
	"encoding/json"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// keyCmd builds NOUN key {list,show,add,delete} for clients and users.
func keyCmd(a *app, k *kinds.Kind) *cobra.Command {
	c := &cobra.Command{Use: "key", Short: "Public keys of a " + k.Name}
	keysPath := func(cl *chef.Client, name string) string {
		return k.ObjectPath(cl, kinds.ID{Name: name}) + "/keys"
	}
	c.AddCommand(&cobra.Command{
		Use:     "list NAME",
		Aliases: []string{"ls"},
		Short:   "List key names",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
			if err != nil {
				return err
			}
			var keys []struct {
				Name    string `json:"name"`
				Expired bool   `json:"expired"`
			}
			if err := cl.Get(cmd.Context(), keysPath(cl, args[0]), &keys); err != nil {
				return err
			}
			if a.output() == "names" {
				names := make([]string, 0, len(keys))
				for _, k := range keys {
					names = append(names, k.Name)
				}
				cli.PrintNames(a.stdout(), names)
				return nil
			}
			return cli.PrintJSON(a.stdout(), keys)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:     "show NAME KEY",
		Aliases: []string{"get"},
		Short:   "Show one key",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
			if err != nil {
				return err
			}
			var out json.RawMessage
			if err := cl.Get(cmd.Context(), keysPath(cl, args[0])+"/"+url.PathEscape(args[1]), &out); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), out)
		},
	})
	var (
		keyName string
		pubFile string
		expires string
	)
	add := &cobra.Command{
		Use:   "add NAME --public-key FILE [--name KEY] [--expires DATE]",
		Short: "Add a public key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
			if err != nil {
				return err
			}
			pub, err := readBody("@" + pubFile)
			if err != nil {
				return err
			}
			body := map[string]string{"name": keyName, "public_key": string(pub), "expiration_date": expires}
			var out json.RawMessage
			if err := cl.Post(cmd.Context(), keysPath(cl, args[0]), body, &out); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), out)
		},
	}
	add.Flags().StringVar(&keyName, "name", "default", "key name")
	add.Flags().StringVar(&pubFile, "public-key", "", "PEM public key file")
	add.Flags().StringVar(&expires, "expires", "infinity", "expiration date (ISO 8601) or infinity")
	_ = add.MarkFlagRequired("public-key")
	c.AddCommand(add)
	c.AddCommand(&cobra.Command{
		Use:     "delete NAME KEY",
		Aliases: []string{"rm"},
		Short:   "Delete a key",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.clientFor(k)
			if err != nil {
				return err
			}
			if err := cl.Delete(cmd.Context(), keysPath(cl, args[0])+"/"+url.PathEscape(args[1]), nil); err != nil {
				return err
			}
			return cli.PrintJSON(a.stdout(), map[string]string{"deleted": args[0] + "/" + args[1]})
		},
	})
	return c
}

// groupMemberCmds adds "group add" and "group remove".
func groupMemberCmds(a *app) []*cobra.Command {
	var users, clients, groups []string
	bind := func(c *cobra.Command) *cobra.Command {
		c.Flags().StringArrayVar(&users, "user", nil, "user name (repeatable)")
		c.Flags().StringArrayVar(&clients, "client", nil, "client name (repeatable)")
		c.Flags().StringArrayVar(&groups, "group", nil, "group name (repeatable)")
		return c
	}
	modify := func(cmd *cobra.Command, name string, add bool) error {
		cl, err := a.orgClient()
		if err != nil {
			return err
		}
		id := kinds.ID{Name: name}
		body, err := kinds.Group.Get(cmd.Context(), cl, id)
		if err != nil {
			return err
		}
		u, c, g := kinds.GroupMembers(body)
		edit := func(cur, change []string) []string {
			set := map[string]bool{}
			for _, n := range cur {
				set[n] = true
			}
			for _, n := range change {
				set[n] = add
			}
			out := []string{}
			for n, keep := range set {
				if keep {
					out = append(out, n)
				}
			}
			return out
		}
		u, c, g = edit(u, users), edit(c, clients), edit(g, groups)
		newBody, _ := json.Marshal(map[string]any{"users": u, "clients": c, "groups": g})
		if err := cl.Put(cmd.Context(), kinds.Group.ObjectPath(cl, id), kinds.GroupWriteBody(name, newBody, true), nil); err != nil {
			return err
		}
		return cli.PrintJSON(a.stdout(), map[string]any{"group": name, "users": u, "clients": c, "groups": g})
	}
	return []*cobra.Command{
		bind(&cobra.Command{
			Use:   "add GROUP --user U --client C --group G",
			Short: "Add members to a group",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return modify(cmd, args[0], true) },
		}),
		bind(&cobra.Command{
			Use:   "remove GROUP --user U --client C --group G",
			Short: "Remove members from a group",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return modify(cmd, args[0], false) },
		}),
	}
}

// orgMemberCmd adds "org member {list,add,remove}".
func orgMemberCmd(a *app) *cobra.Command {
	c := &cobra.Command{Use: "member", Short: "Organisation membership (needs a superuser profile)"}
	c.AddCommand(
		&cobra.Command{
			Use:     "list ORG",
			Aliases: []string{"ls"},
			Short:   "List the users of an organisation",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.defaultClient()
				if err != nil {
					return err
				}
				var members []struct {
					User struct {
						Username string `json:"username"`
					} `json:"user"`
				}
				if err := cl.Get(cmd.Context(), "/organizations/"+url.PathEscape(args[0])+"/users", &members); err != nil {
					return err
				}
				names := make([]string, 0, len(members))
				for _, m := range members {
					names = append(names, m.User.Username)
				}
				return a.printList(names)
			},
		},
		&cobra.Command{
			Use:   "add ORG USER",
			Short: "Add a user to an organisation",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.defaultClient()
				if err != nil {
					return err
				}
				if err := cl.Post(cmd.Context(), "/organizations/"+url.PathEscape(args[0])+"/users", map[string]string{"username": args[1]}, nil); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"organization": args[0], "added": args[1]})
			},
		},
		&cobra.Command{
			Use:     "remove ORG USER",
			Aliases: []string{"rm"},
			Short:   "Remove a user from an organisation",
			Args:    cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				cl, err := a.defaultClient()
				if err != nil {
					return err
				}
				if err := cl.Delete(cmd.Context(), "/organizations/"+url.PathEscape(args[0])+"/users/"+url.PathEscape(args[1]), nil); err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), map[string]string{"organization": args[0], "removed": args[1]})
			},
		},
	)
	return c
}
