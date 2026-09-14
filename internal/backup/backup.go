package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

// Options control Backup and Restore.
type Options struct {
	Workers   int
	SkipUsers bool
	SkipACLs  bool
	// Only / Skip restrict the object kinds handled (singular kind names).
	Only []string
	Skip []string
	// InUse backs up only what a node can reach (plus containers, groups,
	// clients and data bags) instead of every object.
	InUse bool
	// Version is written into the marker.
	Version  string
	Progress func(format string, args ...any)
	Warn     func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Progress == nil {
		o.Progress = func(string, ...any) {}
	}
	if o.Warn == nil {
		o.Warn = func(string, ...any) {}
	}
}

// Wants reports whether a kind is selected by Only/Skip.
func (o *Options) Wants(k *kinds.Kind) bool {
	match := func(list []string) bool {
		for _, n := range list {
			if n == k.Name || n == k.Plural {
				return true
			}
			// data bag items go with data bags
			if k == kinds.DataBagItem && (n == kinds.DataBag.Name || n == kinds.DataBag.Plural) {
				return true
			}
		}
		return false
	}
	if len(o.Only) > 0 && !match(o.Only) {
		return false
	}
	return !match(o.Skip)
}

// Counts is the backup result: objects written per kind.
type Counts struct {
	Org         string         `json:"organization"`
	Dir         string         `json:"dir"`
	Objects     map[string]int `json:"objects"`
	ACLs        int            `json:"acls"`
	ACLsSkipped []string       `json:"acls_skipped"`
	Users       int            `json:"users"`
	Warnings    []string       `json:"warnings"`
	Failed      cli.ItemErrors `json:"failed"`
}

// Backup writes the organisation the client is bound to under root.
func Backup(ctx context.Context, c *chef.Client, root string, opts Options) (*Counts, error) {
	opts.defaults()
	org := c.Org()
	if org == "" {
		return nil, fmt.Errorf("profile has no organisation to back up")
	}
	orgDir := OrgDir(root, org)
	if err := os.MkdirAll(orgDir, 0o755); err != nil {
		return nil, err
	}
	res := &Counts{Org: org, Dir: orgDir, Objects: map[string]int{}}
	fail := func(item string, err error) {
		res.Failed = append(res.Failed, cli.ItemError{Item: item, Err: err.Error()})
	}

	// Organisation-level documents.
	var orgBody json.RawMessage
	if err := c.Get(ctx, "/organizations/"+org, &orgBody); err != nil {
		opts.Warn("organization: %v (writing a minimal org.json)", err)
		orgBody, _ = json.Marshal(map[string]string{"name": org, "full_name": org})
	}
	if err := WriteJSON(filepath.Join(orgDir, "org.json"), orgBody); err != nil {
		return nil, err
	}
	for _, doc := range []struct{ path, file string }{
		{c.OrgPath("/users"), "members.json"},
		{c.OrgPath("/association_requests"), "invitations.json"},
	} {
		var body json.RawMessage
		if err := c.Get(ctx, doc.path, &body); err != nil {
			opts.Warn("%s: %v", doc.file, err)
			continue
		}
		if err := WriteJSON(filepath.Join(orgDir, doc.file), body); err != nil {
			return nil, err
		}
	}

	// Objects and their ACLs: everything, or only what a node can reach.
	var items []transfer.Item
	var acls []transfer.ACLItem
	if opts.InUse {
		opts.Progress("%s: planning what is in use", org)
		plan, err := transfer.InUsePlan(ctx, c, !opts.SkipACLs)
		if err != nil {
			return nil, err
		}
		plan = plan.Filter(opts.Wants)
		for _, w := range plan.Warnings {
			opts.Warn("%s", w)
		}
		items, acls = plan.Items, plan.ACLs
		res.Warnings = plan.Warnings
	} else {
		var err error
		items, acls, err = listAll(ctx, c, opts)
		if err != nil {
			return nil, err
		}
	}
	writeObjects(ctx, c, orgDir, items, opts, res)
	if !opts.SkipACLs {
		writeACLs(ctx, c, orgDir, acls, opts, res)
	}

	// Users are server-level and need a superuser; skip quietly on 403.
	if !opts.SkipUsers {
		ids, err := kinds.User.List(ctx, c)
		switch {
		case chef.IsNotPermitted(err):
			opts.Warn("users: not permitted for this profile; skipping users/ and user_acls/ (use a pivotal profile or --skip-users)")
		case err != nil:
			fail("users", err)
		default:
			opts.Progress("server: %d users", len(ids))
			errs := cli.ForEach(ctx, opts.Workers, ids, kinds.ID.String, func(ctx context.Context, id kinds.ID) error {
				if err := SafeName(id.Name); err != nil {
					return err
				}
				body, err := kinds.User.Get(ctx, c, id)
				if err != nil {
					return err
				}
				if err := WriteJSON(filepath.Join(root, "users", id.Name+".json"), body); err != nil {
					return err
				}
				if opts.SkipACLs {
					return nil
				}
				a, err := acl.Get(ctx, c, "/users/"+id.Name+"/_acl")
				if err != nil {
					return err
				}
				return WriteJSON(filepath.Join(root, "user_acls", id.Name+".json"), a)
			})
			for _, e := range errs {
				fail("user "+e.Item, fmt.Errorf("%s", e.Err))
			}
			res.Users = len(ids) - len(errs)
		}
	}

	marker := MarkerInfo{
		Tool: "gnife", Version: opts.Version, ServerURL: c.ServerRoot(), Org: org,
		APIVersion: chef.APIVersion, Created: time.Now().UTC().Format(time.RFC3339),
	}
	if err := WriteJSON(filepath.Join(orgDir, Marker), marker); err != nil {
		return nil, err
	}
	if res.Failed == nil {
		res.Failed = cli.ItemErrors{}
	}
	if res.ACLsSkipped == nil {
		res.ACLsSkipped = []string{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	return res, nil
}

// listAll lists every object of every selected kind, and the ACLs that go
// with them.
func listAll(ctx context.Context, c *chef.Client, opts Options) ([]transfer.Item, []transfer.ACLItem, error) {
	var items []transfer.Item
	names := map[*kinds.Kind][]string{}
	for _, k := range append(append([]*kinds.Kind{}, JSONKinds...), FilesKinds...) {
		if !opts.Wants(k) {
			continue
		}
		ids, err := k.List(ctx, c)
		if err != nil {
			return nil, nil, fmt.Errorf("listing %s: %w", k.Plural, err)
		}
		if k == kinds.DataBagItem {
			bags, err := kinds.DataBag.List(ctx, c)
			if err != nil {
				return nil, nil, fmt.Errorf("listing data bags: %w", err)
			}
			for _, b := range bags {
				items = append(items, transfer.Item{Kind: kinds.DataBag, ID: b, Reason: "all"})
				names[kinds.DataBag] = append(names[kinds.DataBag], b.Name)
			}
		}
		seen := map[string]bool{}
		for _, id := range ids {
			items = append(items, transfer.Item{Kind: k, ID: id, Reason: "all"})
			if !seen[id.Name] {
				seen[id.Name] = true
				names[k] = append(names[k], id.Name)
			}
		}
	}
	acls := []transfer.ACLItem{{Kind: nil}}
	for _, k := range acl.Kinds {
		if !opts.Wants(k) {
			continue
		}
		list := names[k]
		sort.Strings(list)
		for _, n := range list {
			acls = append(acls, transfer.ACLItem{Kind: k, Name: n})
		}
	}
	return items, acls, nil
}

// writeObjects fetches every item through the worker pool and writes it
// under orgDir. Data bags become directories; cookbooks and artifacts are
// downloaded file by file.
func writeObjects(ctx context.Context, c *chef.Client, orgDir string, items []transfer.Item, opts Options, res *Counts) {
	byKind := map[*kinds.Kind][]transfer.Item{}
	var order []*kinds.Kind
	for _, it := range items {
		if _, ok := byKind[it.Kind]; !ok {
			order = append(order, it.Kind)
		}
		byKind[it.Kind] = append(byKind[it.Kind], it)
	}
	for _, k := range order {
		batch := byKind[k]
		opts.Progress("%s: %d %s", c.Org(), len(batch), k.Plural)
		errs := cli.ForEach(ctx, opts.Workers, batch, func(it transfer.Item) string { return it.ID.String() }, func(ctx context.Context, it transfer.Item) error {
			p, err := ObjectPath(orgDir, k, it.ID)
			if err != nil {
				return err
			}
			switch {
			case k == kinds.DataBag:
				return os.MkdirAll(p, 0o755)
			case k.Files:
				m, err := cookbook.Fetch(ctx, c, k, it.ID)
				if err != nil {
					return err
				}
				return cookbook.Download(ctx, c, m, p, max(2, opts.Workers/4))
			}
			body, err := k.Get(ctx, c, it.ID)
			if err != nil {
				return err
			}
			return WriteJSON(p, body)
		})
		for _, e := range errs {
			res.Failed = append(res.Failed, cli.ItemError{Item: k.Name + " " + e.Item, Err: e.Err})
		}
		res.Objects[k.Plural] += len(batch) - len(errs)
	}
}

// writeACLs fetches and writes ACLs; ones the profile may not read are
// recorded as skipped rather than failed.
func writeACLs(ctx context.Context, c *chef.Client, orgDir string, acls []transfer.ACLItem, opts Options, res *Counts) {
	name := func(a transfer.ACLItem) string {
		if a.Kind == nil {
			return "organization"
		}
		return a.Kind.Name + " " + a.Name
	}
	opts.Progress("%s: %d ACLs", c.Org(), len(acls))
	errs := cli.ForEach(ctx, opts.Workers, acls, name, func(ctx context.Context, a transfer.ACLItem) error {
		path := acl.OrgPath(c)
		if a.Kind != nil {
			path = acl.Path(c, a.Kind, a.Name)
		}
		got, err := acl.Get(ctx, c, path)
		if err != nil {
			return err
		}
		p, err := ACLPath(orgDir, a.Kind, a.Name)
		if err != nil {
			return err
		}
		return WriteJSON(p, got)
	})
	for _, e := range errs {
		if strings.Contains(e.Err, "HTTP 403") || strings.Contains(e.Err, "HTTP 401") {
			res.ACLsSkipped = append(res.ACLsSkipped, e.Item)
			continue
		}
		res.Failed = append(res.Failed, cli.ItemError{Item: "acl " + e.Item, Err: e.Err})
	}
	if len(res.ACLsSkipped) > 0 {
		opts.Warn("%d ACLs not readable by this profile were skipped (a pivotal profile can read them all)", len(res.ACLsSkipped))
	}
	res.ACLs = len(acls) - len(errs)
}
