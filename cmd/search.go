package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/cli"
)

type searchPage struct {
	Total int               `json:"total"`
	Start int               `json:"start"`
	Rows  []json.RawMessage `json:"rows"`
}

func newSearchCmd(a *app) *cobra.Command {
	var (
		rows    int
		start   int
		all     bool
		partial []string
	)
	c := &cobra.Command{
		Use:   "search INDEX [QUERY]",
		Short: "Search an index (node, role, environment, client, or a data bag)",
		Long: `Run a Chef search. QUERY defaults to *:*. With --partial only the named
attributes are returned, as key=dotted.path pairs:

  gnife search node 'chef_environment:prod' --partial name=name --partial ip=ipaddress
  gnife search node --all -o names          # every node name
  gnife search users 'id:*'                 # data bag "users"`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := a.orgClient()
			if err != nil {
				return err
			}
			index := args[0]
			query := "*:*"
			if len(args) == 2 {
				query = args[1]
			}
			var body any
			if len(partial) > 0 {
				m := map[string][]string{}
				for _, p := range partial {
					k, v, ok := strings.Cut(p, "=")
					if !ok {
						return usageErr("--partial wants key=dotted.path, got '%s'", p)
					}
					m[k] = strings.Split(v, ".")
				}
				body = m
			}
			var out []json.RawMessage
			total := 0
			for {
				path := cl.OrgPath(fmt.Sprintf("/search/%s?q=%s&rows=%d&start=%d",
					url.PathEscape(index), url.QueryEscape(query), rows, start))
				var page searchPage
				if body != nil {
					err = cl.Post(cmd.Context(), path, body, &page)
				} else {
					err = cl.Get(cmd.Context(), path, &page)
				}
				if err != nil {
					return err
				}
				total = page.Total
				out = append(out, page.Rows...)
				start += len(page.Rows)
				if !all || len(page.Rows) == 0 || start >= page.Total {
					break
				}
			}
			if a.output() == "names" {
				names := make([]string, 0, len(out))
				for _, r := range out {
					names = append(names, rowName(r))
				}
				cli.PrintNames(a.stdout(), names)
				return nil
			}
			if out == nil {
				out = []json.RawMessage{}
			}
			return cli.PrintJSON(a.stdout(), map[string]any{"total": total, "rows": out})
		},
	}
	c.Flags().IntVar(&rows, "rows", 1000, "rows per page")
	c.Flags().IntVar(&start, "start", 0, "first row")
	c.Flags().BoolVar(&all, "all", false, "follow pagination until every row is fetched")
	c.Flags().StringArrayVar(&partial, "partial", nil, "partial search attribute key=dotted.path (repeatable)")
	return c
}

// rowName pulls a name out of a search row: "name" for full objects,
// data.name for partial results, "id" for data bag items.
func rowName(row json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(row, &m) != nil {
		return string(row)
	}
	for _, k := range []string{"name", "id"} {
		var s string
		if v, ok := m[k]; ok && json.Unmarshal(v, &s) == nil {
			return s
		}
	}
	if data, ok := m["data"]; ok {
		return rowName(data)
	}
	if raw, ok := m["raw_data"]; ok {
		return rowName(raw)
	}
	return string(row)
}
