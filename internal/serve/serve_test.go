package serve

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- protocol 1.1 is SHA-1 by definition
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/chefserver"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// start serves dir on a test listener, loading whatever is there.
func start(t *testing.T, dir string) (*chefserver.Server, *httptest.Server) {
	t.Helper()
	store := &DirStore{Root: dir}
	s := chefserver.NewServer(store)
	if err := store.Load(s); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func client(t *testing.T, ts *httptest.Server, org, name string, key []byte) *chef.Client {
	t.Helper()
	url := ts.URL
	if org != "" {
		url += "/organizations/" + org
	}
	c, err := chef.NewClient(chef.Config{ServerURL: url, ClientName: name, PrivateKeyPEM: key, HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBootstrapWriteRestartRead(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Fresh directory: pivotal is generated and persisted.
	s, ts := start(t, dir)
	priv, err := s.GeneratePivotal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "pivotal.json")); err != nil {
		t.Fatal("pivotal.json not persisted")
	}
	pivotal := client(t, ts, "", "pivotal", append([]byte(nil), priv...))

	// Create an org over the API: org.json, members.json and the validator land on disk.
	var created struct {
		PrivateKey string `json:"private_key"`
		ClientName string `json:"clientname"`
	}
	if err := pivotal.Post(ctx, "/organizations", map[string]string{"name": "acme", "full_name": "Acme"}, &created); err != nil {
		t.Fatal(err)
	}
	if created.ClientName != "acme-validator" || !strings.Contains(created.PrivateKey, "PRIVATE KEY") {
		t.Fatalf("org create response: %+v", created)
	}
	for _, p := range []string{"organizations/acme/org.json", "organizations/acme/members.json", "organizations/acme/clients/acme-validator.json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}

	// The validator registers a client (chef-client bootstrap), which then writes objects.
	validator := client(t, ts, "acme", "acme-validator", []byte(created.PrivateKey))
	var reg struct {
		ChefKey struct {
			PrivateKey string `json:"private_key"`
		} `json:"chef_key"`
	}
	if err := validator.Post(ctx, "/organizations/acme/clients", map[string]any{"name": "node-1", "create_key": true}, &reg); err != nil {
		t.Fatal(err)
	}
	node := client(t, ts, "acme", "node-1", []byte(reg.ChefKey.PrivateKey))
	if err := node.Post(ctx, node.OrgPath("/nodes"), map[string]any{"name": "node-1", "run_list": []string{"recipe[x]"}, "automatic": map[string]any{"platform": "ubuntu"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Post(ctx, node.OrgPath("/data"), map[string]string{"name": "cfg"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Post(ctx, node.OrgPath("/data/cfg"), map[string]any{"id": "main", "port": 8080}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, node.OrgPath("/nodes/node-1/_acl/read"), map[string]any{"read": map[string]any{"actors": []string{"pivotal", "node-1"}, "groups": []string{"admins", "users"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Post(ctx, node.OrgPath("/groups"), map[string]string{"groupname": "ops"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, node.OrgPath("/groups/ops"), map[string]any{"groupname": "ops", "actors": map[string]any{"clients": []string{"node-1"}, "users": []string{}, "groups": []string{}}}, nil); err != nil {
		t.Fatal(err)
	}
	// A cookbook through a sandbox.
	content := []byte("# recipe\n")
	sum := chefserver.Checksum(content)
	var sb struct {
		SandboxID string `json:"sandbox_id"`
		Checksums map[string]struct {
			URL         string `json:"url"`
			NeedsUpload bool   `json:"needs_upload"`
		} `json:"checksums"`
	}
	if err := node.Post(ctx, node.OrgPath("/sandboxes"), map[string]any{"checksums": map[string]any{sum: nil}}, &sb); err != nil {
		t.Fatal(err)
	}
	if err := node.Upload(ctx, sb.Checksums[sum].URL, content, ""); err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, node.OrgPath("/sandboxes/"+sb.SandboxID), map[string]bool{"is_completed": true}, nil); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name": "x-1.0.0", "cookbook_name": "x", "version": "1.0.0", "frozen?": true,
		"metadata":  map[string]any{"name": "x", "version": "1.0.0"},
		"all_files": []map[string]string{{"name": "recipes/default.rb", "path": "recipes/default.rb", "checksum": sum, "specificity": "default"}},
	}
	if err := node.Put(ctx, node.OrgPath("/cookbooks/x/1.0.0"), manifest, nil); err != nil {
		t.Fatal(err)
	}
	ts.Close()

	// Restart from disk and read everything back.
	s2, ts2 := start(t, dir)
	if !s2.HasUser("pivotal") {
		t.Fatal("pivotal not reloaded")
	}
	o := s2.Orgs["acme"]
	if o == nil || o.FullName != "Acme" {
		t.Fatalf("org not reloaded: %+v", o)
	}
	if _, ok := o.Nodes["node-1"]; !ok {
		t.Error("node not reloaded")
	}
	if _, ok := o.Clients["node-1"]; !ok || o.ClientKeys["node-1"]["default"] == "" {
		t.Error("client or its key not reloaded")
	}
	if string(o.DataBags["cfg"]["main"]) == "" {
		t.Error("data bag item not reloaded")
	}
	if g := o.Groups["ops"]; g == nil || len(g.Clients) != 1 {
		t.Errorf("group not reloaded: %+v", g)
	}
	if a := o.ACLs["nodes/node-1"]; len(a["read"].Groups) != 2 {
		t.Errorf("acl not reloaded: %+v", a)
	}
	if _, ok := o.Cookbooks["x"]["1.0.0"]; !ok || !o.Frozen["cookbooks/x/1.0.0"] {
		t.Error("cookbook or frozen flag not reloaded")
	}
	if string(s2.Bookshelf[sum]) != string(content) {
		t.Error("cookbook file content not reloaded")
	}
	// The same client key still authenticates after the restart.
	node2 := client(t, ts2, "acme", "node-1", []byte(reg.ChefKey.PrivateKey))
	var page struct {
		Total int `json:"total"`
	}
	if err := node2.Get(ctx, node2.OrgPath("/search/node?q=platform:ubuntu"), &page); err != nil || page.Total != 1 {
		t.Fatalf("search after restart: %v %+v", err, page)
	}
	var mf map[string]any
	if err := node2.Get(ctx, node2.OrgPath("/cookbooks/x/1.0.0"), &mf); err != nil || mf["frozen?"] != true {
		t.Fatalf("cookbook after restart: %v %v", err, mf["frozen?"])
	}
	// Delete propagates to disk.
	if err := node2.Delete(ctx, node2.OrgPath("/nodes/node-1"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "organizations", "acme", "nodes", "node-1.json")); err == nil {
		t.Error("node file not removed")
	}
}

// TestSigningProtocol11 hand-signs a request the way Chef clients before
// 12.x did (SHA-1, hashed user id, raw RSA "encryption" of the canonical
// string) and checks the server accepts it — and rejects a bad signature.
func TestSigningProtocol11(t *testing.T) {
	s := chefserver.New()
	defer s.Close()
	s.AddOrg("o")
	priv := s.AddClient("o", "old-client", false)
	key, err := chef.ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	do := func(k *rsa.PrivateKey) int {
		path := "/organizations/o/nodes"
		body := []byte{}
		ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		bh := sha1.Sum(body) // #nosec G401
		ph := sha1.Sum([]byte(path))
		uh := sha1.Sum([]byte("old-client"))
		canonical := strings.Join([]string{
			"Method:GET", "Hashed Path:" + base64.StdEncoding.EncodeToString(ph[:]),
			"X-Ops-Content-Hash:" + base64.StdEncoding.EncodeToString(bh[:]),
			"X-Ops-Timestamp:" + ts, "X-Ops-UserId:" + base64.StdEncoding.EncodeToString(uh[:]),
		}, "\n")
		sig, err := rsa.SignPKCS1v15(nil, k, crypto.Hash(0), []byte(canonical))
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, s.HTTP.URL+path, nil)
		req.Header.Set("X-Ops-Sign", "algorithm=sha1;version=1.1;")
		req.Header.Set("X-Ops-Userid", "old-client")
		req.Header.Set("X-Ops-Timestamp", ts)
		req.Header.Set("X-Ops-Content-Hash", base64.StdEncoding.EncodeToString(bh[:]))
		req.Header.Set("X-Ops-Server-API-Version", "1")
		enc := base64.StdEncoding.EncodeToString(sig)
		for i := 0; len(enc) > 0; i++ {
			n := min(60, len(enc))
			req.Header.Set("X-Ops-Authorization-"+string(rune('1'+i)), enc[:n])
			enc = enc[n:]
		}
		resp, err := s.HTTP.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode
	}
	if code := do(key); code != 200 {
		t.Fatalf("v1.1 signature rejected: %d", code)
	}
	other, _ := chef.ParsePrivateKey(s.AddClient("o", "someone-else", false))
	if code := do(other); code != 401 {
		t.Fatalf("wrong key accepted: %d", code)
	}
	_ = kinds.Node
}
