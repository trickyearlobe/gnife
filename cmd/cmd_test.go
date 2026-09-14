package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chefserver"
)

// harness runs gnife commands against a fake server through temp
// credentials and config files.
type harness struct {
	t   *testing.T
	s   *chefserver.Server
	cfg string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := chefserver.New()
	t.Cleanup(s.Close)
	s.Seed("src")
	s.AddOrg("dst")
	dir := t.TempDir()
	var creds strings.Builder
	for _, org := range []string{"src", "dst"} {
		key := s.AddClient(org, "tester", false)
		os.WriteFile(filepath.Join(dir, org+".pem"), key, 0o600)
		fmt.Fprintf(&creds, "[%s]\nclient_name = \"tester\"\nclient_key = \"%s.pem\"\nchef_server_url = \"%s\"\n\n", org, org, s.URL(org))
	}
	os.WriteFile(filepath.Join(dir, "credentials"), []byte(creds.String()), 0o600)
	t.Setenv("CHEF_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("CHEF_PROFILE", "")
	t.Setenv("HOME", dir) // no ~/.chef/context, no trusted certs
	t.Setenv("USERPROFILE", dir)
	cfg := filepath.Join(dir, "config.json")
	t.Setenv("GNIFE_CONFIG", cfg)
	return &harness{t: t, s: s, cfg: cfg}
}

// run executes args and returns stdout, stderr and the exit code.
func (h *harness) run(args ...string) (string, string, int) {
	h.t.Helper()
	a := &app{build: BuildInfo{Version: "test"}}
	root := newRootCmd(a)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	root.SetContext(context.Background())
	// Commands print to os.Stdout; capture it.
	r, w, _ := os.Pipe()
	saved := os.Stdout
	os.Stdout = w
	err := root.Execute()
	w.Close()
	os.Stdout = saved
	var captured bytes.Buffer
	captured.ReadFrom(r)
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	return captured.String() + out.String(), errOut.String(), code
}

func exitCodeOf(err error) int {
	type coder interface{ Unwrap() error }
	_ = coder(nil)
	return exitCode(err)
}

func TestProfileAndConfig(t *testing.T) {
	h := newHarness(t)
	out, _, code := h.run("profile", "list", "-o", "names")
	if code != 0 || out != "dst\nsrc\n" {
		t.Fatalf("profile list: %q %d", out, code)
	}
	if _, _, code := h.run("config", "set", "bogus", "x"); code != 2 {
		t.Fatalf("expected usage exit for bad key, got %d", code)
	}
	if _, _, code := h.run("config", "set", "profile", "src"); code != 0 {
		t.Fatal("config set")
	}
	if _, _, code := h.run("config", "set", "source", "src"); code != 0 {
		t.Fatal("config set source")
	}
	if _, _, code := h.run("config", "set", "dest", "dst"); code != 0 {
		t.Fatal("config set dest")
	}
	out, _, _ = h.run("config", "get", "profile")
	if out != "src\n" {
		t.Fatalf("config get: %q", out)
	}
	data, _ := os.ReadFile(h.cfg)
	if !strings.Contains(string(data), `"profile": "src"`) {
		t.Fatalf("config file: %s", data)
	}
	// Default profile now comes from config.
	out, _, code = h.run("node", "list", "-o", "names")
	if code != 0 || out != "pol-01\nweb-01\n" {
		t.Fatalf("node list: %q %d", out, code)
	}
	out, _, code = h.run("profile", "test")
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("profile test: %q %d", out, code)
	}
	if _, _, code := h.run("node", "list", "-p", "nope"); code != 1 {
		t.Fatalf("unknown profile should exit 1, got %d", code)
	}
}

func TestObjectVerbsAndRaw(t *testing.T) {
	h := newHarness(t)
	h.run("config", "set", "profile", "src")
	out, _, code := h.run("node", "show", "web-01")
	if code != 0 || !strings.Contains(out, `"chef_environment": "prod"`) {
		t.Fatalf("node show: %q %d", out, code)
	}
	f := filepath.Join(t.TempDir(), "n.json")
	os.WriteFile(f, []byte(`{"name":"new-01","run_list":["role[web]"]}`), 0o644)
	if out, _, code := h.run("node", "create", "-f", f); code != 0 || !strings.Contains(out, "new-01") {
		t.Fatalf("node create: %q %d", out, code)
	}
	if _, ok := h.s.Orgs["src"].Nodes["new-01"]; !ok {
		t.Fatal("node not created")
	}
	os.WriteFile(f, []byte(`{"name":"new-01","run_list":[]}`), 0o644)
	if _, _, code := h.run("node", "update", "new-01", "-f", f); code != 0 {
		t.Fatal("node update")
	}
	if strings.Contains(string(h.s.Orgs["src"].Nodes["new-01"]), "role[web]") {
		t.Fatal("update did not replace")
	}
	if _, _, code := h.run("node", "delete", "new-01", "missing"); code != 3 {
		t.Fatalf("partial delete should exit 3, got %d", code)
	}
	if _, ok := h.s.Orgs["src"].Nodes["new-01"]; ok {
		t.Fatal("node not deleted")
	}
	out, _, code = h.run("raw", "get", "/roles")
	if code != 0 || !strings.Contains(out, `"base-role"`) {
		t.Fatalf("raw: %q %d", out, code)
	}
	out, _, code = h.run("raw", "get", "/nodes/nope")
	if code != 1 {
		t.Fatalf("raw 404 should exit 1: %q %d", out, code)
	}
	out, _, code = h.run("search", "node", "name:web-01", "--partial", "env=chef_environment")
	if code != 0 || !strings.Contains(out, `"env": "prod"`) {
		t.Fatalf("search: %q %d", out, code)
	}
	out, _, _ = h.run("databag", "item", "show", "secrets", "db")
	if !strings.Contains(out, "hunter2") {
		t.Fatalf("databag item: %q", out)
	}
	out, _, _ = h.run("cookbook", "list", "-o", "names")
	if out != "app/2.0.0\nbase/1.1.0\nunused/0.1.0\n" {
		t.Fatalf("cookbook list: %q", out)
	}
	out, _, _ = h.run("acl", "show", "node", "web-01")
	if !strings.Contains(out, `"ops"`) {
		t.Fatalf("acl show: %q", out)
	}
}

func TestCopyDryRunAndCopy(t *testing.T) {
	h := newHarness(t)
	h.run("config", "set", "source", "src")
	h.run("config", "set", "dest", "dst")
	out, _, code := h.run("node", "copy", "web-01", "--deps", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %q %d", out, code)
	}
	var plan struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 6 || len(h.s.Orgs["dst"].Nodes) != 0 {
		t.Fatalf("dry run plan %v, dst nodes %d", plan.Items, len(h.s.Orgs["dst"].Nodes))
	}
	out, _, code = h.run("role", "copy", "web", "--deps")
	if code != 0 {
		t.Fatalf("copy: %q %d", out, code)
	}
	if _, ok := h.s.Orgs["dst"].Roles["web"]; !ok {
		t.Fatal("role not copied")
	}
	if _, ok := h.s.Orgs["dst"].Cookbooks["app"]["2.0.0"]; !ok {
		t.Fatal("cookbook dependency not copied")
	}
	if _, ok := h.s.Orgs["dst"].Cookbooks["base"]["1.1.0"]; !ok {
		t.Fatal("transitive cookbook not copied")
	}
	if _, _, code := h.run("node", "copy", "web-01", "--from", "src", "--to", "src"); code != 2 {
		t.Fatalf("same org should be a usage error, got %d", code)
	}
}

func TestBackupRestoreCommands(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(t.TempDir(), "bk")
	out, _, code := h.run("backup", "--dir", dir, "-p", "src", "--archive")
	if code != 0 {
		t.Fatalf("backup: %q %d", out, code)
	}
	if _, err := os.Stat(dir + ".tar.gz"); err != nil {
		t.Fatal("archive missing")
	}
	out, _, code = h.run("restore", "--dir", dir+".tar.gz", "-p", "dst", "--dry-run")
	if code != 0 || !strings.Contains(out, `"plan"`) {
		t.Fatalf("restore dry run: %q %d", out, code)
	}
	out, _, code = h.run("restore", "--dir", dir, "-p", "dst")
	if code != 0 {
		t.Fatalf("restore: %q %d", out, code)
	}
	if _, ok := h.s.Orgs["dst"].Nodes["web-01"]; !ok {
		t.Fatal("node not restored")
	}
	if _, ok := h.s.Orgs["dst"].Cookbooks["unused"]["0.1.0"]; !ok {
		t.Fatal("cookbook not restored")
	}
}

func TestOrgCreatePromptsAndSavesValidatorKey(t *testing.T) {
	h := newHarness(t)
	// A superuser profile.
	dir := t.TempDir()
	key := h.s.AddUser("boss", true)
	os.WriteFile(filepath.Join(dir, "boss.pem"), key, 0o600)
	creds := fmt.Sprintf("[boss]\nclient_name = \"boss\"\nclient_key = \"%s\"\nchef_server_url = \"%s\"\n", filepath.Join(dir, "boss.pem"), h.s.HTTP.URL)
	os.WriteFile(filepath.Join(dir, "credentials"), []byte(creds), 0o600)
	t.Setenv("CHEF_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))

	// Prompts read stdin: name, then full name.
	r, w, _ := os.Pipe()
	saved := os.Stdin
	os.Stdin = r
	stdin = nil
	defer func() { stdin = nil }()
	fmt.Fprint(w, "acme\nAcme Ltd\n")
	w.Close()
	keyFile := filepath.Join(dir, "acme-validator.pem")
	out, _, code := h.run("org", "create", "-p", "boss", "--validator-keyfile", keyFile)
	os.Stdin = saved
	if code != 0 {
		t.Fatalf("org create: %q %d", out, code)
	}
	if !strings.Contains(out, `"created": "acme"`) || !strings.Contains(out, `"full_name": "Acme Ltd"`) {
		t.Fatalf("output: %s", out)
	}
	if strings.Contains(out, "private_key") {
		t.Fatal("key should have gone to the file, not stdout")
	}
	if data, err := os.ReadFile(keyFile); err != nil || len(data) == 0 {
		t.Fatalf("validator key file: %v", err)
	}
	if o, ok := h.s.Orgs["acme"]; !ok || o.FullName != "Acme Ltd" {
		t.Fatalf("org not created: %+v", h.s.Orgs["acme"])
	}
	// Name as argument, full name from flag: no prompting; key goes to ~/.chef by default.
	out, _, code = h.run("org", "create", "beta", "-p", "boss", "--full-name", "Beta Inc")
	if code != 0 || strings.Contains(out, `"private_key"`) {
		t.Fatalf("org create beta: %q %d", out, code)
	}
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, ".chef", "beta-validator.pem")
	if data, err := os.ReadFile(def); err != nil || len(data) == 0 {
		t.Fatalf("default validator key file %s: %v", def, err)
	}
	// An existing key file blocks creation before anything is created.
	if _, _, code := h.run("org", "create", "beta2", "-p", "boss", "--full-name", "x", "--validator-keyfile", def); code == 0 {
		t.Fatal("expected refusal to overwrite an existing key file")
	}
	if _, ok := h.s.Orgs["beta2"]; ok {
		t.Fatal("org must not be created when the key file cannot be written")
	}
}

func TestCloneInUse(t *testing.T) {
	h := newHarness(t)
	h.run("config", "set", "source", "src")
	h.run("config", "set", "dest", "dst")
	out, _, code := h.run("clone", "--in-use", "--dry-run")
	if code != 0 || !strings.Contains(out, `"cookbook"`) || len(h.s.Orgs["dst"].Nodes) != 0 {
		t.Fatalf("dry run: %q %d", out, code)
	}
	if _, _, code := h.run("clone", "--in-use", "--purge"); code != 2 {
		t.Fatalf("--purge with --in-use should be a usage error, got %d", code)
	}
	out, _, code = h.run("clone", "--in-use")
	if code != 0 {
		t.Fatalf("clone: %q %d", out, code)
	}
	o := h.s.Orgs["dst"]
	if _, ok := o.Cookbooks["app"]["2.0.0"]; !ok {
		t.Fatal("app not cloned")
	}
	if _, ok := o.Cookbooks["base"]["1.0.0"]; !ok {
		t.Fatal("base 1.0.0 not cloned")
	}
	if _, ok := o.Cookbooks["base"]["1.1.0"]; ok {
		t.Fatal("base 1.1.0 is unused and should not be cloned")
	}
	if _, ok := o.Cookbooks["unused"]; ok {
		t.Fatal("unused cookbook should not be cloned")
	}
	if len(o.Nodes) != 2 || o.PolicyGroups["dev"]["demo"] != "rev1" || o.Groups["ops"] == nil {
		t.Fatalf("nodes %d groups %v policy groups %v", len(o.Nodes), o.Groups["ops"], o.PolicyGroups)
	}
	if string(o.DataBags["secrets"]["db"]) == "" {
		t.Fatal("data bag not cloned")
	}
}

func TestEditCommandsUseEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("editor script is a shell script")
	}
	h := newHarness(t)
	h.run("config", "set", "profile", "src")
	// An "editor" that rewrites the file with sed.
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	os.WriteFile(editor, []byte("#!/bin/sh\nsed -i.bak \"$EDIT_EXPR\" \"$1\"\n"), 0o755)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	t.Setenv("EDIT_EXPR", `s/"ops"/"ops", "admins"/`)
	out, _, code := h.run("acl", "edit", "node", "web-01")
	if code != 0 || !strings.Contains(out, `"updated"`) {
		t.Fatalf("acl edit: %q %d", out, code)
	}
	if got := h.s.Orgs["src"].ACLs["nodes/web-01"]["read"].Groups; len(got) != 3 {
		t.Fatalf("acl not updated: %v", got)
	}

	t.Setenv("EDIT_EXPR", `s/"prod"/"staging"/`)
	if out, _, code := h.run("node", "edit", "web-01"); code != 0 {
		t.Fatalf("node edit: %q %d", out, code)
	}
	if !strings.Contains(string(h.s.Orgs["src"].Nodes["web-01"]), `"staging"`) {
		t.Fatal("node not updated")
	}

	// No change: nothing written, no error.
	t.Setenv("EDIT_EXPR", `s/nothing-here/x/`)
	if _, _, code := h.run("role", "edit", "web"); code != 0 {
		t.Fatal("unchanged edit should succeed")
	}
	// Invalid JSON after editing: refused.
	t.Setenv("EDIT_EXPR", `s/{/{{/`)
	if _, _, code := h.run("role", "edit", "web"); code == 0 {
		t.Fatal("broken JSON must be rejected")
	}
	// No edit on name-only kinds.
	if _, _, code := h.run("databag", "edit", "secrets"); code == 0 {
		t.Fatal("databag edit should not exist")
	}
}
