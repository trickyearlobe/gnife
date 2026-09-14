package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/chefserver"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

func setup(t *testing.T) (*chefserver.Server, *chef.Client, *chef.Client) {
	t.Helper()
	s := chefserver.New()
	t.Cleanup(s.Close)
	s.Seed("src")
	s.AddOrg("dst")
	mk := func(org string) *chef.Client {
		key := s.AddClient(org, "tester", false)
		c, err := chef.NewClient(chef.Config{ServerURL: s.URL(org), ClientName: "tester", PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return s, mk("src"), mk("dst")
}

func keys(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Key())
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestPlanNodeWithDeps(t *testing.T) {
	_, src, _ := setup(t)
	plan, err := BuildPlan(context.Background(), src, []Item{{Kind: kinds.Node, ID: kinds.ID{Name: "web-01"}}}, PlanOptions{Deps: true, ACL: true})
	if err != nil {
		t.Fatal(err)
	}
	// prod pins base = 1.0.0; the depsolver must honour it, and the
	// environment's own constraint expansion agrees.
	want := "cookbook:app/2.0.0 cookbook:base/1.0.0 environment:prod node:web-01 role:base-role role:web"
	if got := keys(plan.Items); got != want {
		t.Fatalf("plan:\n got %s\nwant %s", got, want)
	}
	// Restore order: environment, cookbooks, roles, node.
	var order []string
	for _, it := range plan.Items {
		order = append(order, it.Kind.Name)
	}
	if got := strings.Join(order, ","); got != "environment,cookbook,cookbook,role,role,node" {
		t.Fatalf("order: %s", got)
	}
	if len(plan.ACLs) != 6 {
		t.Fatalf("acls: %d", len(plan.ACLs))
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings: %v", plan.Warnings)
	}
}

func TestPlanWithoutDeps(t *testing.T) {
	_, src, _ := setup(t)
	plan, err := BuildPlan(context.Background(), src, []Item{{Kind: kinds.Node, ID: kinds.ID{Name: "web-01"}}}, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(plan.Items); got != "node:web-01" {
		t.Fatalf("plan: %s", got)
	}
}

func TestPlanPolicyNodeAndBagAndGroupAndLatestCookbook(t *testing.T) {
	_, src, _ := setup(t)
	plan, err := BuildPlan(context.Background(), src, []Item{
		{Kind: kinds.Node, ID: kinds.ID{Name: "pol-01"}},
		{Kind: kinds.DataBag, ID: kinds.ID{Name: "secrets"}},
		{Kind: kinds.Group, ID: kinds.ID{Name: "ops"}},
		{Kind: kinds.Cookbook, ID: kinds.ID{Name: "base"}}, // no version: latest
	}, PlanOptions{Deps: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "artifact:app/abc123 client:web-01 cookbook:base/1.1.0 databag-item:secrets/db databag:secrets group:admins group:ops node:pol-01 policy:demo/rev1 policygroup:dev"
	if got := keys(plan.Items); got != want {
		t.Fatalf("plan:\n got %s\nwant %s", got, want)
	}
	for _, it := range plan.Items {
		if it.Kind == kinds.PolicyGroup && !strings.Contains(string(it.Body), `"demo"`) {
			t.Fatalf("policy group body should be filtered to demo: %s", it.Body)
		}
	}
}

func TestExecuteCopiesEverything(t *testing.T) {
	s, src, dst := setup(t)
	ctx := context.Background()
	plan, err := BuildPlan(ctx, src, []Item{
		{Kind: kinds.Node, ID: kinds.ID{Name: "web-01"}},
		{Kind: kinds.Node, ID: kinds.ID{Name: "pol-01"}},
		{Kind: kinds.Group, ID: kinds.ID{Name: "ops"}},
		{Kind: kinds.DataBag, ID: kinds.ID{Name: "secrets"}},
	}, PlanOptions{Deps: true, ACL: true})
	if err != nil {
		t.Fatal(err)
	}
	res := Execute(ctx, NewServerSource(src), dst, plan, ExecOptions{Workers: 4})
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}
	if len(res.Written) != len(plan.Items) {
		t.Fatalf("written %d of %d: %v", len(res.Written), len(plan.Items), res.Written)
	}
	o := s.Orgs["dst"]
	if _, ok := o.Nodes["web-01"]; !ok {
		t.Fatal("node not copied")
	}
	if _, ok := o.Cookbooks["app"]["2.0.0"]; !ok {
		t.Fatal("cookbook app not copied")
	}
	if _, ok := o.Cookbooks["base"]["1.0.0"]; !ok {
		t.Fatal("cookbook base 1.0.0 not copied")
	}
	if _, ok := o.Cookbooks["unused"]; ok {
		t.Fatal("unused cookbook should not be copied")
	}
	if _, ok := o.Artifacts["app"]["abc123"]; !ok {
		t.Fatal("artifact not copied")
	}
	if o.PolicyGroups["dev"]["demo"] != "rev1" {
		t.Fatalf("policy group: %v", o.PolicyGroups)
	}
	if string(o.DataBags["secrets"]["db"]) == "" {
		t.Fatal("data bag item not copied")
	}
	// alice is not a member of dst, so she is dropped from ops with a warning;
	// web-01 (copied) and admins (exists) stay.
	g := o.Groups["ops"]
	if fmt.Sprint(g.Users, g.Clients, g.Groups) != "[] [web-01] [admins]" {
		t.Fatalf("group ops: %+v", g)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, `dropping user 'alice'`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected alice warning, got %v", res.Warnings)
	}
	// ACL on the node was applied, minus nothing (web-01 client and ops group exist now).
	a := o.ACLs["nodes/web-01"]
	if fmt.Sprint(a["read"].Actors, a["read"].Groups) != "[pivotal web-01] [admins ops]" {
		t.Fatalf("node acl: %+v", a["read"])
	}
	// The copied client carries its public key.
	if o.ClientKeys["web-01"]["default"] == "" {
		t.Fatal("client key not copied")
	}

	// Second run: everything exists, nothing written, nothing failed.
	res2 := Execute(ctx, NewServerSource(src), dst, plan, ExecOptions{Workers: 4})
	if len(res2.Failed) != 0 || len(res2.Written) != 3 { // groups and policy groups are always (re)written
		t.Fatalf("second run: written %v failed %v", res2.Written, res2.Failed)
	}
	if len(res2.Skipped) != len(plan.Items)-3 {
		t.Fatalf("skipped: %v", res2.Skipped)
	}

	// Overwrite replaces the node with the source's version after a local edit.
	s.Orgs["dst"].Nodes["web-01"] = json.RawMessage(`{"name":"web-01","run_list":["recipe[edited]"]}`)
	res3 := Execute(ctx, NewServerSource(src), dst, plan, ExecOptions{Workers: 4, Overwrite: true})
	if len(res3.Failed) != 0 {
		t.Fatalf("overwrite: %v", res3.Failed)
	}
	if strings.Contains(string(s.Orgs["dst"].Nodes["web-01"]), "edited") {
		t.Fatal("overwrite did not replace the node")
	}
}

func TestExecuteReportsPerItemFailures(t *testing.T) {
	_, src, dst := setup(t)
	plan := &Plan{Items: []Item{
		{Kind: kinds.Node, ID: kinds.ID{Name: "web-01"}},
		{Kind: kinds.Node, ID: kinds.ID{Name: "does-not-exist"}},
	}}
	res := Execute(context.Background(), NewServerSource(src), dst, plan, ExecOptions{Workers: 2})
	if len(res.Written) != 1 || len(res.Failed) != 1 || res.Failed[0].Item != "node does-not-exist" {
		t.Fatalf("written %v failed %v", res.Written, res.Failed)
	}
	if res.Err() == nil {
		t.Fatal("expected partial error")
	}
}

func TestUSAGsAreNeverListedOrCopied(t *testing.T) {
	s, src, dst := setup(t)
	s.Orgs["src"].Groups["00000000000029542963513dbc4b2853"] = &chefserver.Group{Users: []string{"alice"}, Clients: []string{}, Groups: []string{}}
	s.Orgs["src"].Groups["ops"].Groups = append(s.Orgs["src"].Groups["ops"].Groups, "00000000000029542963513dbc4b2853")
	ids, err := kinds.Group.List(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if kinds.IsUSAG(id.Name) {
			t.Fatalf("USAG listed: %s", id)
		}
	}
	plan, err := InUsePlan(context.Background(), src, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plan.Items {
		if it.Kind == kinds.Group && kinds.IsUSAG(it.ID.Name) {
			t.Fatalf("USAG planned: %s", it.ID)
		}
	}
	res := Execute(context.Background(), NewServerSource(src), dst, plan, ExecOptions{Workers: 2})
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}
	if _, ok := s.Orgs["dst"].Groups["00000000000029542963513dbc4b2853"]; ok {
		t.Fatal("USAG copied")
	}
}

func TestOverwriteSkipsImmutableArtifacts(t *testing.T) {
	_, src, dst := setup(t)
	plan := &Plan{Items: []Item{{Kind: kinds.Artifact, ID: kinds.ID{Name: "app", Sub: "abc123"}}}}
	for i := 0; i < 2; i++ {
		res := Execute(context.Background(), NewServerSource(src), dst, plan, ExecOptions{Workers: 2, Overwrite: true})
		if len(res.Failed) != 0 {
			t.Fatalf("run %d: %v", i, res.Failed)
		}
		if i == 0 && len(res.Written) != 1 || i == 1 && len(res.Skipped) != 1 {
			t.Fatalf("run %d: written %v skipped %v", i, res.Written, res.Skipped)
		}
	}
}
