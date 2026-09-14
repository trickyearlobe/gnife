package cookbook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/chefserver"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

func TestClassify(t *testing.T) {
	cases := map[string][2]string{
		"metadata.rb":                       {"root_files/metadata.rb", "default"},
		"recipes/default.rb":                {"recipes/default.rb", "default"},
		"templates/default/a.erb":           {"templates/a.erb", "default"},
		"templates/a.erb":                   {"templates/a.erb", "root_default"},
		"files/host-web/x.txt":              {"files/x.txt", "host-web"},
		"spec/unit/recipes/default_spec.rb": {"spec/default_spec.rb", "default"},
	}
	for in, want := range cases {
		name, spec := classify(in)
		if name != want[0] || spec != want[1] {
			t.Errorf("%s: got %s/%s want %s/%s", in, name, spec, want[0], want[1])
		}
	}
}

func TestSafeRelPath(t *testing.T) {
	for _, ok := range []string{"a", "a/b", "recipes/default.rb", ".gitignore"} {
		if _, err := SafeRelPath(ok); err != nil {
			t.Errorf("%q should be safe: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../b", "a/./b", `a\b`, "a//b", "C:x"} {
		if _, err := SafeRelPath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestParseDirName(t *testing.T) {
	id, ok := ParseDirName("chef-client-12.3.4")
	if !ok || id.Name != "chef-client" || id.Sub != "12.3.4" {
		t.Fatalf("got %+v %v", id, ok)
	}
	if _, ok := ParseDirName("nohyphen"); ok {
		t.Fatal("expected failure")
	}
}

func client(t *testing.T, s *chefserver.Server, org string) *chef.Client {
	t.Helper()
	key := s.AddClient(org, "tester", true)
	c, err := chef.NewClient(chef.Config{ServerURL: s.URL(org), ClientName: "tester", PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDownloadUploadRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	s.AddOrg("dst")
	src := client(t, s, "src")
	dst := client(t, s, "dst")

	id := kinds.ID{Name: "app", Sub: "2.0.0"}
	m, err := Fetch(ctx, src, kinds.Cookbook, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 4 || m.Dependencies()["base"] != ">= 1.0" {
		t.Fatalf("manifest: %d files, deps %v", len(m.Files), m.Dependencies())
	}
	dir := filepath.Join(t.TempDir(), DirName(id))
	if err := Download(ctx, src, m, dir, 4); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "templates", "default", "app.conf.erb")); err != nil || string(data) != "conf <%= @x %>" {
		t.Fatalf("template: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "metadata.json")); err != nil {
		t.Fatal("metadata.json should have been generated")
	}

	// Upload the directory into the other org and compare manifests.
	up, err := UploadDir(ctx, dst, kinds.Cookbook, kinds.ID{Name: "app"}, dir, UploadOptions{Workers: 2, Freeze: true})
	if err != nil {
		t.Fatal(err)
	}
	if up.ID.Sub != "2.0.0" {
		t.Fatalf("version from metadata.json: %s", up.ID.Sub)
	}
	got, err := Fetch(ctx, dst, kinds.Cookbook, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Frozen {
		t.Fatal("expected frozen")
	}
	if len(got.Files) != 5 { // + generated metadata.json
		t.Fatalf("expected 5 files, got %d", len(got.Files))
	}
	for _, f := range got.Files {
		if f.Path == "templates/default/app.conf.erb" && (f.Name != "templates/app.conf.erb" || f.Specificity != "default") {
			t.Fatalf("bad entry %+v", f)
		}
	}
	// Frozen: re-upload without force fails, with force succeeds.
	if _, err := UploadDir(ctx, dst, kinds.Cookbook, kinds.ID{Name: "app"}, dir, UploadOptions{}); !chef.IsConflict(err) {
		t.Fatalf("expected conflict on frozen, got %v", err)
	}
	if _, err := UploadDir(ctx, dst, kinds.Cookbook, kinds.ID{Name: "app"}, dir, UploadOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	// Server-to-server copy of an artifact.
	if err := Copy(ctx, src, dst, kinds.Artifact, kinds.ID{Name: "app", Sub: "abc123"}, UploadOptions{Workers: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(ctx, dst, kinds.Artifact, kinds.ID{Name: "app", Sub: "abc123"}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDirNeedsMetadataJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "metadata.rb"), []byte(`name "x"`), 0o644)
	if _, _, err := LoadDir(kinds.Cookbook, kinds.ID{Name: "x"}, dir); err == nil {
		t.Fatal("expected error without metadata.json")
	}
}
