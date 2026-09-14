package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/deps"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// Item is one object to transfer.
type Item struct {
	Kind *kinds.Kind
	ID   kinds.ID
	// Reason explains why the item is in the plan ("requested", "dependency of node web-01").
	Reason string
	// Body overrides the source body (restore, filtered policy groups).
	Body json.RawMessage
	// resolved marks cookbooks the depsolver chose and roles a run list
	// expansion visited; their dependencies are already in the closure and
	// must not be re-expanded (which would ignore environment pins).
	resolved bool
}

// ACLItem is one ACL to transfer; Kind nil is the organisation ACL.
type ACLItem struct {
	Kind *kinds.Kind
	Name string
}

// Plan is an ordered set of items.
type Plan struct {
	Items    []Item
	ACLs     []ACLItem
	Warnings []string
}

// PlanOptions control dependency expansion.
type PlanOptions struct {
	Deps        bool
	ACL         bool
	Environment string // environment for role depsolving (default _default)
}

// Key identifies an item in the plan.
func (it Item) Key() string { return it.Kind.Name + ":" + it.ID.String() }

// BuildPlan expands the requested items against the source organisation.
func BuildPlan(ctx context.Context, src *chef.Client, requested []Item, opts PlanOptions) (*Plan, error) {
	b := &planner{ctx: ctx, src: src, opts: opts, seen: map[string]bool{}, plan: &Plan{}, versions: map[string][]string{}}
	queue := append([]Item(nil), requested...)
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if it.Reason == "" {
			it.Reason = "requested"
		}
		more, err := b.add(it)
		if err != nil {
			return nil, err
		}
		queue = append(queue, more...)
	}
	if opts.ACL {
		b.addACLs()
	}
	b.plan.sort()
	return b.plan, nil
}

type planner struct {
	ctx      context.Context
	src      *chef.Client
	opts     PlanOptions
	seen     map[string]bool
	plan     *Plan
	versions map[string][]string // cookbook name -> versions in source
}

func (b *planner) warnf(format string, args ...any) {
	b.plan.Warnings = append(b.plan.Warnings, fmt.Sprintf(format, args...))
}

// add records the item and returns the items it depends on.
func (b *planner) add(it Item) ([]Item, error) {
	// Resolve "latest" for files kinds requested without a version.
	if it.Kind.Files && it.ID.Sub == "" {
		versions, err := b.cookbookVersions(it.Kind, it.ID.Name)
		if err != nil {
			return nil, err
		}
		if it.Kind == kinds.Cookbook {
			latest, ok := deps.Latest(versions)
			if !ok {
				return nil, fmt.Errorf("cookbook %s: no versions on source", it.ID.Name)
			}
			it.ID.Sub = latest
		} else {
			return nil, fmt.Errorf("artifact %s: identifier required", it.ID.Name)
		}
	}
	if b.seen[it.Key()] {
		return nil, nil
	}
	b.seen[it.Key()] = true
	b.plan.Items = append(b.plan.Items, it)

	because := fmt.Sprintf("dependency of %s %s", it.Kind.Name, it.ID)
	dep := func(k *kinds.Kind, id kinds.ID) Item { return Item{Kind: k, ID: id, Reason: because} }
	var out []Item

	// Data bags and their items always travel together.
	switch it.Kind {
	case kinds.DataBag:
		items, err := b.bagItems(it.ID.Name)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			out = append(out, Item{Kind: kinds.DataBagItem, ID: item, Reason: "item of data bag " + it.ID.Name})
		}
		return out, nil
	case kinds.DataBagItem:
		out = append(out, Item{Kind: kinds.DataBag, ID: kinds.ID{Name: it.ID.Name}, Reason: "bag of " + it.ID.String()})
	}
	if !b.opts.Deps {
		return out, nil
	}

	switch it.Kind {
	case kinds.Node:
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		var n struct {
			Env         string   `json:"chef_environment"`
			RunList     []string `json:"run_list"`
			PolicyName  string   `json:"policy_name"`
			PolicyGroup string   `json:"policy_group"`
		}
		_ = json.Unmarshal(body, &n)
		if n.Env != "" && n.Env != "_default" {
			out = append(out, dep(kinds.Environment, kinds.ID{Name: n.Env}))
		}
		if n.PolicyName != "" && n.PolicyGroup != "" {
			more, err := b.policyAssignment(n.PolicyGroup, n.PolicyName, because)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
			return out, nil
		}
		more, err := b.runList(n.RunList, n.Env, because)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)

	case kinds.Role:
		if it.resolved {
			break
		}
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		var r deps.Role
		_ = json.Unmarshal(body, &r)
		lists := [][]string{r.RunList}
		for _, l := range r.EnvRunLists {
			lists = append(lists, l)
		}
		for _, l := range lists {
			more, err := b.runList(l, b.opts.Environment, because)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}

	case kinds.Environment:
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		var e struct {
			CookbookVersions map[string]string `json:"cookbook_versions"`
		}
		_ = json.Unmarshal(body, &e)
		for name, constraint := range e.CookbookVersions {
			versions, err := b.cookbookVersions(kinds.Cookbook, name)
			if err != nil {
				return nil, err
			}
			best, ok := deps.Best(versions, constraint)
			if !ok {
				b.warnf("environment %s: no version of cookbook %s satisfies '%s' on source", it.ID, name, constraint)
				continue
			}
			out = append(out, dep(kinds.Cookbook, kinds.ID{Name: name, Sub: best}))
		}

	case kinds.Cookbook:
		if it.resolved {
			break
		}
		m, err := cookbook.Fetch(b.ctx, b.src, kinds.Cookbook, it.ID)
		if err != nil {
			return nil, err
		}
		for name, constraint := range m.Dependencies() {
			versions, err := b.cookbookVersions(kinds.Cookbook, name)
			if err != nil {
				return nil, err
			}
			best, ok := deps.Best(versions, constraint)
			if !ok {
				b.warnf("cookbook %s depends on %s '%s' but no such version is on source", it.ID, name, constraint)
				continue
			}
			out = append(out, dep(kinds.Cookbook, kinds.ID{Name: name, Sub: best}))
		}

	case kinds.Policy:
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		for _, id := range policyArtifacts(body) {
			out = append(out, dep(kinds.Artifact, id))
		}

	case kinds.PolicyGroup:
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		for name, rev := range kinds.PolicyGroupPolicies(body) {
			out = append(out, dep(kinds.Policy, kinds.ID{Name: name, Sub: rev}))
		}

	case kinds.Group:
		if it.resolved {
			break
		}
		body, err := b.body(it)
		if err != nil {
			return nil, err
		}
		_, clients, groups := kinds.GroupMembers(body)
		for _, g := range groups {
			if !kinds.IsUSAG(g) {
				out = append(out, dep(kinds.Group, kinds.ID{Name: g}))
			}
		}
		for _, c := range clients {
			out = append(out, dep(kinds.Client, kinds.ID{Name: c}))
		}
	}
	return out, nil
}

func (b *planner) body(it Item) (json.RawMessage, error) {
	if it.Body != nil {
		return it.Body, nil
	}
	return it.Kind.Get(b.ctx, b.src, it.ID)
}

func (b *planner) bagItems(bag string) ([]kinds.ID, error) {
	var m map[string]json.RawMessage
	if err := b.src.Get(b.ctx, kinds.DataBagItem.ObjectPath(b.src, kinds.ID{Name: bag}), &m); err != nil {
		return nil, err
	}
	var ids []kinds.ID
	for name := range m {
		ids = append(ids, kinds.ID{Name: bag, Sub: name})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Sub < ids[j].Sub })
	return ids, nil
}

func (b *planner) cookbookVersions(k *kinds.Kind, name string) ([]string, error) {
	key := k.Name + ":" + name
	if v, ok := b.versions[key]; ok {
		return v, nil
	}
	body, err := k.Get(b.ctx, b.src, kinds.ID{Name: name})
	if err != nil {
		if chef.IsNotFound(err) {
			b.versions[key] = nil
			return nil, nil
		}
		return nil, err
	}
	var m map[string]struct {
		Versions []struct {
			Version    string `json:"version"`
			Identifier string `json:"identifier"`
		} `json:"versions"`
	}
	_ = json.Unmarshal(body, &m)
	var versions []string
	for _, e := range m {
		for _, v := range e.Versions {
			if v.Version != "" {
				versions = append(versions, v.Version)
			} else {
				versions = append(versions, v.Identifier)
			}
		}
	}
	b.versions[key] = versions
	return versions, nil
}

// runList expands roles and solves cookbooks for a run list.
func (b *planner) runList(runList []string, env, because string) ([]Item, error) {
	if len(runList) == 0 {
		return nil, nil
	}
	ex, err := deps.Expand(b.ctx, runList, env, func(ctx context.Context, name string) (json.RawMessage, error) {
		return kinds.Role.Get(ctx, b.src, kinds.ID{Name: name})
	})
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, r := range ex.Roles {
		// Expand already walked nested roles in this environment; solving
		// them again in _default would add versions the node never uses.
		out = append(out, Item{Kind: kinds.Role, ID: kinds.ID{Name: r}, Reason: because, resolved: true})
	}
	for _, m := range ex.Missing {
		b.warnf("%s: role %s is in the run list but not on source", because, m)
	}
	solved, err := deps.Solve(b.ctx, b.src, env, ex.Recipes)
	if err != nil {
		// The depsolver refuses when a recipe's cookbook is missing; fall
		// back to the latest version of whatever cookbooks do exist.
		b.warnf("%s: depsolver failed (%v); falling back to latest versions", because, err)
		solved = map[string]string{}
		for _, r := range ex.Recipes {
			name := deps.ParseRunListItem(r).Cookbook()
			versions, err := b.cookbookVersions(kinds.Cookbook, name)
			if err != nil {
				return nil, err
			}
			if latest, ok := deps.Latest(versions); ok {
				solved[name] = latest
			} else {
				b.warnf("%s: cookbook %s is not on source", because, name)
			}
		}
	}
	for name, version := range solved {
		out = append(out, Item{Kind: kinds.Cookbook, ID: kinds.ID{Name: name, Sub: version}, Reason: because, resolved: true})
	}
	return out, nil
}

// policyAssignment adds the policy revision a group assigns to a name, plus
// a policy group item restricted to that one assignment.
func (b *planner) policyAssignment(group, policy, because string) ([]Item, error) {
	body, err := kinds.PolicyGroup.Get(b.ctx, b.src, kinds.ID{Name: group})
	if err != nil {
		if chef.IsNotFound(err) {
			b.warnf("%s: policy group %s not on source", because, group)
			return nil, nil
		}
		return nil, err
	}
	rev, ok := kinds.PolicyGroupPolicies(body)[policy]
	if !ok {
		b.warnf("%s: policy group %s does not assign policy %s", because, group, policy)
		return nil, nil
	}
	filtered, _ := json.Marshal(map[string]any{
		"policies": map[string]any{policy: map[string]string{"revision_id": rev}},
	})
	return []Item{
		{Kind: kinds.Policy, ID: kinds.ID{Name: policy, Sub: rev}, Reason: because},
		{Kind: kinds.PolicyGroup, ID: kinds.ID{Name: group}, Reason: because, Body: filtered},
	}, nil
}

// policyArtifacts lists the cookbook artifacts a policy revision locks.
func policyArtifacts(body json.RawMessage) []kinds.ID {
	var p struct {
		Locks map[string]struct {
			Identifier string `json:"identifier"`
		} `json:"cookbook_locks"`
	}
	_ = json.Unmarshal(body, &p)
	var ids []kinds.ID
	for name, l := range p.Locks {
		if l.Identifier != "" {
			ids = append(ids, kinds.ID{Name: name, Sub: l.Identifier})
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Name < ids[j].Name })
	return ids
}

func (b *planner) addACLs() {
	seen := map[string]bool{}
	for _, it := range b.plan.Items {
		var ok bool
		for _, k := range acl.Kinds {
			if k == it.Kind {
				ok = true
			}
		}
		if !ok {
			continue
		}
		key := it.Kind.Name + ":" + it.ID.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		b.plan.ACLs = append(b.plan.ACLs, ACLItem{Kind: it.Kind, Name: it.ID.Name})
	}
}

// sort orders items by restore order, then name.
func (p *Plan) sort() {
	sort.SliceStable(p.Items, func(i, j int) bool {
		a, b := p.Items[i], p.Items[j]
		if a.Kind.Order != b.Kind.Order {
			return a.Kind.Order < b.Kind.Order
		}
		if a.ID.Name != b.ID.Name {
			return a.ID.Name < b.ID.Name
		}
		return a.ID.Sub < b.ID.Sub
	})
}

// Summary renders the plan for --dry-run.
func (p *Plan) Summary() map[string]any {
	items := make([]map[string]string, 0, len(p.Items))
	for _, it := range p.Items {
		items = append(items, map[string]string{"kind": it.Kind.Name, "id": it.ID.String(), "reason": it.Reason})
	}
	acls := make([]string, 0, len(p.ACLs))
	for _, a := range p.ACLs {
		if a.Kind == nil {
			acls = append(acls, "organization")
		} else {
			acls = append(acls, a.Kind.Name+":"+a.Name)
		}
	}
	warnings := p.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{"items": items, "acls": acls, "warnings": warnings}
}
