package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/chefserver"
)

func superClient(t *testing.T, s *chefserver.Server, org string) *chef.Client {
	t.Helper()
	key := s.AddUser("pivotal-"+org, true)
	c, err := chef.NewClient(chef.Config{ServerURL: s.URL(org), ClientName: "pivotal-" + org, PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func orgClient(t *testing.T, s *chefserver.Server, org string) *chef.Client {
	t.Helper()
	key := s.AddClient(org, "backup-client", true)
	c, err := chef.NewClient(chef.Config{ServerURL: s.URL(org), ClientName: "backup-client", PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sortedKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBackupRestoreRoundTripWithRename(t *testing.T) {
	ctx := context.Background()
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	s.AddUser("pivotal", true) // present in the backup, must never be restored
	src := superClient(t, s, "src")

	root := t.TempDir()
	counts, err := Backup(ctx, src, root, Options{Workers: 4, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts.Failed) != 0 {
		t.Fatalf("backup failures: %v", counts.Failed)
	}
	if counts.Objects["cookbooks"] != 4 || counts.Objects["nodes"] != 2 || counts.Users != 3 {
		t.Fatalf("counts: %+v", counts)
	}
	for _, p := range []string{
		"organizations/src/org.json", "organizations/src/members.json", "organizations/src/.gnife.json",
		"organizations/src/nodes/web-01.json", "organizations/src/roles/base-role.json",
		"organizations/src/cookbooks/app-2.0.0/recipes/default.rb", "organizations/src/cookbooks/app-2.0.0/metadata.json",
		"organizations/src/cookbook_artifacts/app-abc123/recipes/default.rb",
		"organizations/src/data_bags/secrets/db.json", "organizations/src/data_bags/empty",
		"organizations/src/policies/demo-rev1.json", "organizations/src/policy_groups/dev.json",
		"organizations/src/acls/nodes/web-01.json", "organizations/src/acls/organization.json",
		"organizations/src/groups/ops.json", "organizations/src/clients/web-01.json",
		"users/alice.json", "user_acls/alice.json", "users/pivotal.json",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}

	// Rename the organisation on disk, then restore into a fresh org on a
	// second server under yet another name, creating it.
	if err := os.Rename(filepath.Join(root, "organizations", "src"), filepath.Join(root, "organizations", "renamed")); err != nil {
		t.Fatal(err)
	}
	s2 := chefserver.New()
	defer s2.Close()
	dst := superClient(t, s2, "restored")
	keyFile := filepath.Join(t.TempDir(), "restored-validator.pem")
	res, err := Restore(ctx, dst, root, RestoreOptions{Options: Options{Workers: 4}, CreateOrg: true, ValidatorKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if res.ValidatorKeyfile != keyFile {
		t.Fatalf("validator key file: %q", res.ValidatorKeyfile)
	}
	if data, err := os.ReadFile(keyFile); err != nil || !strings.Contains(string(data), "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("validator key: %.40q %v", data, err)
	}
	if _, ok := s2.Users["pivotal"]; ok {
		t.Fatal("pivotal must never be restored")
	}
	if err := res.Err(); err != nil {
		t.Fatalf("restore failures: %v / %v", res.Failed, res.Result.Failed)
	}
	if res.From != filepath.Join(root, "organizations", "renamed") {
		t.Fatalf("from: %s", res.From)
	}
	o, ok := s2.Orgs["restored"]
	if !ok {
		t.Fatal("organisation was not created")
	}
	so := s.Orgs["src"]
	if !reflect.DeepEqual(sortedKeys(o.Nodes), sortedKeys(so.Nodes)) {
		t.Errorf("nodes: %v vs %v", sortedKeys(o.Nodes), sortedKeys(so.Nodes))
	}
	if !reflect.DeepEqual(sortedKeys(o.Roles), sortedKeys(so.Roles)) {
		t.Errorf("roles: %v", sortedKeys(o.Roles))
	}
	for name, versions := range so.Cookbooks {
		if !reflect.DeepEqual(sortedKeys(o.Cookbooks[name]), sortedKeys(versions)) {
			t.Errorf("cookbook %s: %v vs %v", name, sortedKeys(o.Cookbooks[name]), sortedKeys(versions))
		}
	}
	if _, ok := o.Artifacts["app"]["abc123"]; !ok {
		t.Error("artifact missing")
	}
	if o.PolicyGroups["dev"]["demo"] != "rev1" {
		t.Errorf("policy group: %v", o.PolicyGroups)
	}
	if string(o.DataBags["secrets"]["db"]) == "" {
		t.Error("data bag item missing")
	}
	if _, ok := o.DataBags["empty"]; !ok {
		t.Error("empty data bag missing")
	}
	if _, ok := s2.Users["alice"]; !ok {
		t.Error("user alice not restored")
	}
	if got := o.Members; len(got) != 1 || got[0] != "alice" {
		t.Errorf("members: %v", got)
	}
	g := o.Groups["ops"]
	if g == nil || len(g.Users) != 1 || g.Users[0] != "alice" || len(g.Clients) != 1 || g.Clients[0] != "web-01" {
		t.Errorf("group ops: %+v", g)
	}
	if o.ClientKeys["web-01"]["default"] == "" {
		t.Error("client key not restored")
	}
	a := o.ACLs["nodes/web-01"]
	if len(a["read"].Groups) != 2 {
		t.Errorf("node acl: %+v", a["read"])
	}
	if _, ok := o.Containers["nodes"]; !ok {
		t.Error("containers missing")
	}
}

func TestRestoreDryRunPurgeAndArchive(t *testing.T) {
	ctx := context.Background()
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	src := orgClient(t, s, "src")
	root := t.TempDir()
	if _, err := Backup(ctx, src, root, Options{Workers: 2, SkipUsers: true}); err != nil {
		t.Fatal(err)
	}

	// Destination has an extra node and role the backup lacks.
	s.AddOrg("dst")
	s.Orgs["dst"].Nodes["stray"] = json.RawMessage(`{"name":"stray"}`)
	s.Orgs["dst"].Roles["stray-role"] = json.RawMessage(`{"name":"stray-role"}`)
	dst := orgClient(t, s, "dst")

	dry, err := Restore(ctx, dst, root, RestoreOptions{Options: Options{Workers: 2, SkipUsers: true}, Purge: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Plan == nil || len(s.Orgs["dst"].Nodes) != 1 {
		t.Fatal("dry run must not change anything")
	}
	purge := dry.Plan["purge"].([]string)
	sort.Strings(purge)
	if !reflect.DeepEqual(purge, []string{"node stray", "role stray-role"}) {
		t.Fatalf("purge plan: %v", purge)
	}

	// Archive round trip: restore from the tar.gz with purge.
	tgz := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Archive(root, tgz); err != nil {
		t.Fatal(err)
	}
	extracted, err := Extract(tgz)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(extracted)
	res, err := Restore(ctx, dst, extracted, RestoreOptions{Options: Options{Workers: 2, SkipUsers: true}, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("restore: %v %v", res.Failed, res.Result.Failed)
	}
	if _, ok := s.Orgs["dst"].Nodes["stray"]; ok {
		t.Error("stray node not purged")
	}
	if _, ok := s.Orgs["dst"].Nodes["web-01"]; !ok {
		t.Error("web-01 not restored")
	}
	if _, ok := s.Orgs["dst"].Environments["_default"]; !ok {
		t.Error("_default must never be purged")
	}
	if _, ok := s.Orgs["dst"].Groups["admins"]; !ok {
		t.Error("admins must never be purged")
	}
	if _, ok := s.Orgs["dst"].Clients["backup-client"]; !ok {
		t.Error("the restoring client must never be purged")
	}
	if len(res.Purged) != 2 {
		t.Errorf("purged: %v", res.Purged)
	}
}

func TestRestoreOnlyAndOrgSelection(t *testing.T) {
	ctx := context.Background()
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	root := t.TempDir()
	if _, err := Backup(ctx, orgClient(t, s, "src"), root, Options{Workers: 2, SkipUsers: true}); err != nil {
		t.Fatal(err)
	}
	// Two org dirs and no --org: ambiguous unless one matches the destination.
	os.MkdirAll(filepath.Join(root, "organizations", "other"), 0o755)
	s.AddOrg("dst")
	dst := orgClient(t, s, "dst")
	if _, err := Restore(ctx, dst, root, RestoreOptions{Options: Options{Workers: 2, SkipUsers: true}}); err == nil {
		t.Fatal("expected ambiguity error")
	}
	res, err := Restore(ctx, dst, root, RestoreOptions{Options: Options{Workers: 2, SkipUsers: true, Only: []string{"role"}}, OrgDir: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Err(); err != nil {
		t.Fatal(err)
	}
	if len(s.Orgs["dst"].Roles) != 2 || len(s.Orgs["dst"].Nodes) != 0 {
		t.Fatalf("only roles expected: roles %d nodes %d", len(s.Orgs["dst"].Roles), len(s.Orgs["dst"].Nodes))
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	// A tar with an entry escaping the target directory must be refused.
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.tar.gz")
	writeEvilTar(t, evil)
	if _, err := Extract(evil); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestBackupInUse(t *testing.T) {
	ctx := context.Background()
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	root := t.TempDir()
	counts, err := Backup(ctx, orgClient(t, s, "src"), root, Options{Workers: 4, SkipUsers: true, InUse: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts.Failed) != 0 {
		t.Fatalf("failures: %v", counts.Failed)
	}
	org := filepath.Join(root, "organizations", "src")
	for _, p := range []string{
		"cookbooks/app-2.0.0", "cookbooks/base-1.0.0", // web-01 in prod, which pins base = 1.0.0
		"cookbook_artifacts/app-abc123", "policies/demo-rev1.json", "policy_groups/dev.json",
		"roles/base-role.json", "roles/web.json", "environments/prod.json",
		"nodes/web-01.json", "nodes/pol-01.json",
		"clients/web-01.json", "groups/ops.json", "containers/nodes.json",
		"data_bags/secrets/db.json", "data_bags/empty",
		"acls/organization.json", "acls/nodes/web-01.json", "acls/cookbooks/app.json",
	} {
		if _, err := os.Stat(filepath.Join(org, p)); err != nil {
			t.Errorf("expected %s", p)
		}
	}
	for _, p := range []string{"cookbooks/unused-0.1.0", "cookbooks/base-1.1.0"} {
		if _, err := os.Stat(filepath.Join(org, p)); err == nil {
			t.Errorf("%s is not in use and should be absent", p)
		}
	}
	if counts.Objects["cookbooks"] != 2 || counts.Objects["roles"] != 2 {
		t.Fatalf("counts: %v", counts.Objects)
	}
}
