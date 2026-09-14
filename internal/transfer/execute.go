package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// ExecOptions control Execute.
type ExecOptions struct {
	Overwrite bool // replace objects that already exist
	Force     bool // with Overwrite: replace frozen cookbook versions too
	Workers   int
	Progress  func(format string, args ...any)
	Warn      func(format string, args ...any)
}

// Result is what Execute did.
type Result struct {
	Written  []string       `json:"written"`
	Skipped  []string       `json:"skipped"`
	Failed   cli.ItemErrors `json:"failed"`
	ACLs     []string       `json:"acls"`
	Warnings []string       `json:"warnings"`
	mu       sync.Mutex
}

func (r *Result) written(s string) { r.mu.Lock(); r.Written = append(r.Written, s); r.mu.Unlock() }
func (r *Result) skipped(s string) { r.mu.Lock(); r.Skipped = append(r.Skipped, s); r.mu.Unlock() }
func (r *Result) warn(s string)    { r.mu.Lock(); r.Warnings = append(r.Warnings, s); r.mu.Unlock() }

// Err returns nil when everything succeeded, else a partial error.
func (r *Result) Err() error {
	if len(r.Failed) == 0 {
		return nil
	}
	return cli.Partial(r.Failed)
}

// Finish sorts the lists and nils out nothing so JSON shows arrays.
func (r *Result) Finish() *Result {
	sort.Strings(r.Written)
	sort.Strings(r.Skipped)
	sort.Strings(r.ACLs)
	for _, p := range []*[]string{&r.Written, &r.Skipped, &r.ACLs, &r.Warnings} {
		if *p == nil {
			*p = []string{}
		}
	}
	if r.Failed == nil {
		r.Failed = cli.ItemErrors{}
	}
	return r
}

// Execute writes every item of the plan into dst, in restore order.
func Execute(ctx context.Context, src Source, dst *chef.Client, plan *Plan, opts ExecOptions) *Result {
	if opts.Progress == nil {
		opts.Progress = func(string, ...any) {}
	}
	if opts.Warn == nil {
		opts.Warn = func(string, ...any) {}
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	res := &Result{}
	for _, w := range plan.Warnings {
		res.warn(w)
	}
	principals := newPrincipals(dst)

	// Batch consecutive items of one kind so each kind runs concurrently
	// but kinds stay in order.
	var groups []Item
	for i := 0; i < len(plan.Items); {
		j := i
		for j < len(plan.Items) && plan.Items[j].Kind == plan.Items[i].Kind {
			j++
		}
		batch := plan.Items[i:j]
		k := batch[0].Kind
		i = j
		if k == kinds.Group {
			// Shells now; membership after every principal exists.
			groups = append(groups, batch...)
			errs := cli.ForEach(ctx, opts.Workers, batch, itemName, func(ctx context.Context, it Item) error {
				return kinds.PutGroupShell(ctx, dst, it.ID.Name)
			})
			res.Failed = append(res.Failed, errs...)
			continue
		}
		opts.Progress("%s: %d %s", dst.Org(), len(batch), k.Plural)
		errs := cli.ForEach(ctx, opts.Workers, batch, itemName, func(ctx context.Context, it Item) error {
			return execItem(ctx, src, dst, it, opts, res)
		})
		res.Failed = append(res.Failed, errs...)
	}

	if len(groups) > 0 {
		opts.Progress("%s: %d groups (membership)", dst.Org(), len(groups))
		errs := cli.ForEach(ctx, opts.Workers, groups, itemName, func(ctx context.Context, it Item) error {
			body := it.Body
			var err error
			if body == nil {
				body, err = src.Get(ctx, kinds.Group, it.ID)
				if err != nil {
					return err
				}
			}
			users, clients, subgroups := kinds.GroupMembers(body)
			keep := func(kind string, names []string) []string {
				out := []string{}
				for _, n := range names {
					switch {
					case kind == "group" && kinds.IsUSAG(n):
						// the destination makes its own when users are associated
					case principals.has(ctx, kind, n):
						out = append(out, n)
					default:
						res.warn(fmt.Sprintf("group %s: dropping %s '%s', not present on destination", it.ID.Name, kind, n))
					}
				}
				return out
			}
			filtered, _ := json.Marshal(map[string]any{
				"users": keep("user", users), "clients": keep("client", clients), "groups": keep("group", subgroups),
			})
			if err := dst.Put(ctx, kinds.Group.ObjectPath(dst, it.ID), kinds.GroupWriteBody(it.ID.Name, filtered, true), nil); err != nil {
				return err
			}
			res.written(itemName(it))
			return nil
		})
		res.Failed = append(res.Failed, errs...)
	}

	if len(plan.ACLs) > 0 {
		opts.Progress("%s: %d ACLs", dst.Org(), len(plan.ACLs))
		errs := cli.ForEach(ctx, opts.Workers, plan.ACLs, aclName, func(ctx context.Context, a ACLItem) error {
			src1, err := src.ACL(ctx, a.Kind, a.Name)
			if err != nil {
				return err
			}
			filtered, dropped := acl.Filter(src1, func(kind, name string) bool {
				if kind == "actor" {
					return principals.has(ctx, "user", name) || principals.has(ctx, "client", name)
				}
				return principals.has(ctx, kind, name)
			})
			for _, d := range dropped {
				res.warn(fmt.Sprintf("acl %s: dropping %s, not present on destination", aclName(a), d))
			}
			path := acl.OrgPath(dst)
			if a.Kind != nil {
				path = acl.Path(dst, a.Kind, a.Name)
			}
			if err := acl.Put(ctx, dst, path, filtered); err != nil {
				return err
			}
			res.mu.Lock()
			res.ACLs = append(res.ACLs, aclName(a))
			res.mu.Unlock()
			return nil
		})
		res.Failed = append(res.Failed, errs...)
	}
	sort.Slice(res.Failed, func(i, j int) bool { return res.Failed[i].Item < res.Failed[j].Item })
	return res.Finish()
}

func itemName(it Item) string { return it.Kind.Name + " " + it.ID.String() }

func aclName(a ACLItem) string {
	if a.Kind == nil {
		return "organization"
	}
	return a.Kind.Name + " " + a.Name
}

func execItem(ctx context.Context, src Source, dst *chef.Client, it Item, opts ExecOptions, res *Result) error {
	k := it.Kind
	name := itemName(it)

	if k.Files {
		exists, err := k.Exists(ctx, dst, it.ID)
		if err != nil {
			return err
		}
		// Artifacts are content-addressed by identifier, hence immutable:
		// an existing one is by definition already the same.
		if exists && (!opts.Overwrite || k == kinds.Artifact) {
			res.skipped(name)
			return nil
		}
		m, read, err := src.Cookbook(ctx, k, it.ID)
		if err != nil {
			return err
		}
		fileWorkers := max(2, opts.Workers/4)
		if err := cookbook.Upload(ctx, dst, m, read, cookbook.UploadOptions{Force: opts.Force, Freeze: m.Frozen, Workers: fileWorkers}); err != nil {
			return err
		}
		res.written(name)
		return nil
	}

	body := it.Body
	if body == nil {
		var err error
		body, err = src.Get(ctx, k, it.ID)
		if err != nil {
			return err
		}
	}
	switch k {
	case kinds.PolicyGroup:
		// Assignment is additive and idempotent; no exists/overwrite dance.
		if err := k.Put(ctx, dst, it.ID, body, true); err != nil {
			return err
		}
		res.written(name)
		return nil
	case kinds.Organization:
		// Never rename or recreate an organisation from a copy.
		return fmt.Errorf("organisations are not copied; create the destination organisation first")
	}
	exists, err := k.Exists(ctx, dst, it.ID)
	if err != nil {
		return err
	}
	if exists && !opts.Overwrite {
		res.skipped(name)
		return nil
	}
	if err := k.Put(ctx, dst, it.ID, body, exists); err != nil {
		return err
	}
	if k == kinds.Client || k == kinds.User {
		keys, err := src.Keys(ctx, k, it.ID.Name)
		if err != nil {
			return fmt.Errorf("keys: %w", err)
		}
		if err := kinds.PutKeys(ctx, dst, k, it.ID.Name, keys); err != nil {
			return fmt.Errorf("keys: %w", err)
		}
	}
	res.written(name)
	return nil
}

// principals lazily loads the destination's users, clients and groups.
type principals struct {
	dst  *chef.Client
	mu   sync.Mutex
	sets map[string]map[string]bool
	errs map[string]error
}

func newPrincipals(dst *chef.Client) *principals {
	return &principals{dst: dst, sets: map[string]map[string]bool{}, errs: map[string]error{}}
}

// has reports whether the destination has the principal; kind is user,
// client or group. Groups written earlier in this run are re-listed once.
func (p *principals) has(ctx context.Context, kind, name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	set, ok := p.sets[kind]
	if !ok {
		set = p.load(ctx, kind)
		p.sets[kind] = set
	}
	if set[name] {
		return true
	}
	if kind == "group" || kind == "client" {
		// May have been created by this run after the first listing.
		set = p.load(ctx, kind)
		p.sets[kind] = set
		return set[name]
	}
	return false
}

func (p *principals) load(ctx context.Context, kind string) map[string]bool {
	set := map[string]bool{}
	switch kind {
	case "user":
		var members []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		if err := p.dst.Get(ctx, p.dst.OrgPath("/users"), &members); err != nil {
			p.errs[kind] = err
			return set
		}
		for _, m := range members {
			set[m.User.Username] = true
		}
		set["pivotal"] = true
	case "client":
		ids, err := kinds.Client.List(ctx, p.dst)
		if err != nil {
			p.errs[kind] = err
			return set
		}
		for _, id := range ids {
			set[id.Name] = true
		}
	case "group":
		ids, err := kinds.Group.List(ctx, p.dst)
		if err != nil {
			p.errs[kind] = err
			return set
		}
		for _, id := range ids {
			set[id.Name] = true
		}
	}
	return set
}
