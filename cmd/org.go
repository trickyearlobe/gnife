package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/credentials"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// orgCreateCmd replaces the generic create: the fields are few enough to
// prompt for, and the response carries the validator's private key, which
// must not be lost.
func orgCreateCmd(a *app) *cobra.Command {
	var (
		file     string
		fullName string
		keyFile  string
	)
	c := &cobra.Command{
		Use:   "create [NAME] [-f FILE | --full-name NAME] [--validator-keyfile FILE]",
		Short: "Create an organisation (prompts for anything not given)",
		Long: `Create an organisation. Values come from -f FILE ({"name","full_name"}),
from the arguments and flags, or from prompts. The private key of the new
NAME-validator client is written to ~/.chef/NAME-validator.pem (mode 0600),
or to --validator-keyfile; it cannot be retrieved again later, so an existing
file is never overwritten.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.defaultClient()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if file != "" {
				body, err := readJSONBody(file, kinds.Organization)
				if err != nil {
					return err
				}
				if name == "" {
					name = kinds.Field(body, "name")
				}
				if fullName == "" {
					fullName = kinds.Field(body, "full_name")
				}
			}
			if name == "" {
				if name, err = prompt("Organisation name", ""); err != nil {
					return err
				}
			}
			if name == "" {
				return usageErr("an organisation name is required")
			}
			if fullName == "" && file == "" {
				if fullName, err = prompt("Full name", name); err != nil {
					return err
				}
			}
			if fullName == "" {
				fullName = name
			}
			if keyFile == "" {
				keyFile = filepath.Join(credentials.ChefDir(), name+"-validator.pem")
			}
			if _, err := os.Stat(keyFile); err == nil {
				return fmt.Errorf("%s already exists; move it or pass --validator-keyfile", keyFile)
			}
			created, err := kinds.CreateOrganization(cmd.Context(), cl, name, fullName)
			if err != nil {
				return err
			}
			out := map[string]any{"created": name, "full_name": fullName}
			if created.ClientName != "" {
				out["validator"] = created.ClientName
			}
			if created.URI != "" {
				out["uri"] = created.URI
			}
			if created.PrivateKey != "" {
				if err := kinds.SaveValidatorKey(keyFile, created.PrivateKey); err != nil {
					out["private_key"] = created.PrivateKey // never lose it
					_ = cli.PrintJSON(a.stdout(), out)
					return fmt.Errorf("writing %s: %w (key printed above)", keyFile, err)
				}
				out["validator_keyfile"] = keyFile
			}
			return cli.PrintJSON(a.stdout(), out)
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "JSON file with name and full_name, or - for stdin")
	c.Flags().StringVar(&fullName, "full-name", "", "full (display) name; defaults to the name")
	c.Flags().StringVar(&keyFile, "validator-keyfile", "", "where to write the validator's private key (default ~/.chef/NAME-validator.pem)")
	return c
}

// stdin is shared by every prompt so buffered input is not lost between them.
var stdin *bufio.Reader

// prompt asks on stderr and reads one line from stdin; empty input returns def.
func prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	if stdin == nil {
		stdin = bufio.NewReader(os.Stdin)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading %s: %w", strings.ToLower(label), err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// replaceCommand swaps a generated verb for a custom one.
func replaceCommand(parent *cobra.Command, name string, with *cobra.Command) {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			parent.RemoveCommand(c)
			break
		}
	}
	parent.AddCommand(with)
}
