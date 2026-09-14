package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
)

func newRawCmd(a *app) *cobra.Command {
	var (
		body    string
		root    bool
		include bool
	)
	c := &cobra.Command{
		Use:   "raw",
		Short: "Send a raw request to the API",
		Long: `Send a signed request and print the response body. PATH is relative to
the profile's organisation (like knife raw); use --root for paths relative
to the server root such as /users or /organizations.

  gnife raw get /nodes
  gnife raw put /nodes/web-01 -d @node.json
  gnife raw post /environments -d '{"name":"qa"}'
  gnife raw get --root /organizations`,
	}
	c.PersistentFlags().StringVarP(&body, "data", "d", "", "request body: a literal, @file, or - for stdin")
	c.PersistentFlags().BoolVar(&root, "root", false, "PATH is relative to the server root, not the organisation")
	c.PersistentFlags().BoolVarP(&include, "include", "i", false, "print status and headers along with the body")

	run := func(method string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			cl, err := a.defaultClient()
			if err != nil {
				return err
			}
			path := args[0]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			if !root {
				if cl.Org() == "" {
					return usageErr("profile has no organisation; use --root")
				}
				path = cl.OrgPath(path)
			}
			var data []byte
			if body != "" {
				data, err = readBody(body)
				if err != nil {
					return err
				}
			}
			resp, err := cl.Do(cmd.Context(), method, path, data)
			if resp != nil {
				if perr := printRaw(a, resp, include || method == http.MethodHead); perr != nil {
					return perr
				}
			}
			return err
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodHead} {
		c.AddCommand(&cobra.Command{
			Use:   strings.ToLower(m) + " PATH",
			Short: m + " a path",
			Args:  cobra.ExactArgs(1),
			RunE:  run(m),
		})
	}
	return c
}

// readBody resolves -d: "-" is stdin, "@file" is a file, anything else is literal.
func readBody(spec string) ([]byte, error) {
	switch {
	case spec == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(spec, "@"):
		data, err := os.ReadFile(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("reading body: %w", err)
		}
		return data, nil
	default:
		return []byte(spec), nil
	}
}

func printRaw(a *app, resp *chef.Response, include bool) error {
	var bodyOut any
	if len(resp.Body) > 0 {
		if json.Valid(resp.Body) {
			bodyOut = json.RawMessage(resp.Body)
		} else {
			bodyOut = string(resp.Body)
		}
	}
	if !include {
		if bodyOut == nil {
			return nil
		}
		if s, ok := bodyOut.(string); ok {
			fmt.Fprintln(a.stdout(), s)
			return nil
		}
		return cli.PrintJSON(a.stdout(), bodyOut)
	}
	headers := map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}
	return cli.PrintJSON(a.stdout(), map[string]any{
		"status":  resp.Status,
		"headers": headers,
		"body":    bodyOut,
	})
}
