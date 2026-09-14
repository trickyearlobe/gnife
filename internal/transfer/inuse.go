package transfer

import (
	"context"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// InUsePlan plans everything a node on the source can reach — its
// environment, roles, the cookbook versions the depsolver picks for its run
// list, or its policy revision, artifacts and group — plus the structural
// kinds whose usage cannot be inferred from the server: containers, groups,
// clients and data bags. Unreferenced cookbook versions, roles,
// environments, policies and artifacts are left behind.
func InUsePlan(ctx context.Context, src *chef.Client, withACLs bool) (*Plan, error) {
	var roots []Item
	structural := []*kinds.Kind{kinds.Container, kinds.Group, kinds.Client, kinds.DataBag}
	for _, k := range structural {
		ids, err := k.List(ctx, src)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			// resolved: groups are copied whole, not expanded into members.
			roots = append(roots, Item{Kind: k, ID: id, Reason: "structural", resolved: true})
		}
	}
	nodes, err := kinds.Node.List(ctx, src)
	if err != nil {
		return nil, err
	}
	for _, id := range nodes {
		roots = append(roots, Item{Kind: kinds.Node, ID: id, Reason: "node"})
	}
	plan, err := BuildPlan(ctx, src, roots, PlanOptions{Deps: true, ACL: withACLs})
	if err != nil {
		return nil, err
	}
	if withACLs {
		plan.ACLs = append([]ACLItem{{Kind: nil}}, plan.ACLs...)
	}
	return plan, nil
}

// Filter keeps only the items (and ACLs) of kinds keep accepts.
func (p *Plan) Filter(keep func(*kinds.Kind) bool) *Plan {
	out := &Plan{Warnings: p.Warnings}
	for _, it := range p.Items {
		if keep(it.Kind) {
			out.Items = append(out.Items, it)
		}
	}
	for _, a := range p.ACLs {
		if a.Kind == nil || keep(a.Kind) {
			out.ACLs = append(out.ACLs, a)
		}
	}
	return out
}
