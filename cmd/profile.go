package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
)

type profileSummary struct {
	Name       string `json:"name"`
	ServerURL  string `json:"chef_server_url"`
	ClientName string `json:"client_name"`
	ClientKey  string `json:"client_key"` // path, or "(inline)"
	Org        string `json:"organization,omitempty"`
	SSLVerify  bool   `json:"ssl_verify"`
	Default    bool   `json:"default"`
}

func newProfileCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles", "credential", "credentials"},
		Short:   "Inspect the profiles in ~/.chef/credentials",
	}
	summary := func(name string) (profileSummary, error) {
		p, err := a.profile(name)
		if err != nil {
			return profileSummary{}, err
		}
		key := p.KeyPath()
		if key == "" && p.ClientKey != "" {
			key = "(inline)"
		}
		return profileSummary{
			Name: p.Name, ServerURL: p.ServerURL, ClientName: p.ClientName,
			ClientKey: key, Org: p.Org(), SSLVerify: !p.InsecureSkipVerify(),
			Default: name == a.profileName(),
		}, nil
	}
	c.AddCommand(
		&cobra.Command{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List profiles",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				f, err := a.credentialsFile()
				if err != nil {
					return err
				}
				if a.output() == "names" {
					cli.PrintNames(a.stdout(), f.Order)
					return nil
				}
				out := make([]profileSummary, 0, len(f.Order))
				for _, n := range f.Order {
					s, err := summary(n)
					if err != nil {
						return err
					}
					out = append(out, s)
				}
				return cli.PrintJSON(a.stdout(), out)
			},
		},
		&cobra.Command{
			Use:     "show [NAME]",
			Aliases: []string{"get"},
			Short:   "Show one profile (key material is never printed)",
			Args:    cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name := a.profileName()
				if len(args) == 1 {
					name = args[0]
				}
				s, err := summary(name)
				if err != nil {
					return err
				}
				return cli.PrintJSON(a.stdout(), s)
			},
		},
		&cobra.Command{
			Use:   "test [NAME]",
			Short: "Make an authenticated request with the profile",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name := a.profileName()
				if len(args) == 1 {
					name = args[0]
				}
				c, err := a.client(name)
				if err != nil {
					return err
				}
				path := "/users/" + c.ClientName()
				if c.Org() != "" {
					path = c.OrgPath("/environments/_default")
				}
				if err := c.Get(cmd.Context(), path, nil); err != nil {
					return fmt.Errorf("profile '%s': %w", name, err)
				}
				return cli.PrintJSON(a.stdout(), map[string]any{
					"profile": name, "server": c.ServerRoot(), "organization": c.Org(),
					"client": c.ClientName(), "ok": true,
				})
			},
		},
	)
	return c
}
