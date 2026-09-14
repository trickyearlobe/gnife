package chef_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/chefserver"
)

func TestSplitServerURL(t *testing.T) {
	cases := []struct{ in, root, org string }{
		{"https://chef.example.com/organizations/myorg", "https://chef.example.com", "myorg"},
		{"https://chef.example.com/organizations/myorg/", "https://chef.example.com", "myorg"},
		{"https://chef.example.com:8443/prefix/organizations/o2/nodes", "https://chef.example.com:8443/prefix", "o2"},
		{"https://chef.example.com", "https://chef.example.com", ""},
		{"http://localhost:4000/", "http://localhost:4000", ""},
	}
	for _, c := range cases {
		root, org, err := chef.SplitServerURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if root.String() != c.root || org != c.org {
			t.Errorf("%s: got %s / %q, want %s / %q", c.in, root, org, c.root, c.org)
		}
	}
	for _, bad := range []string{"", "chef.example.com", "ftp://x/organizations/a"} {
		if _, _, err := chef.SplitServerURL(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestSignedRequestsAreAccepted(t *testing.T) {
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	key := s.AddClient("src", "tester", true)

	c, err := chef.NewClient(chef.Config{ServerURL: s.URL("src"), ClientName: "tester", PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var nodes map[string]string
	if err := c.Get(context.Background(), c.OrgPath("/nodes"), &nodes); err != nil {
		t.Fatal(err)
	}
	if _, ok := nodes["web-01"]; !ok {
		t.Fatalf("expected web-01 in %v", nodes)
	}

	// A body must be hashed into the signature too.
	var created map[string]string
	body := json.RawMessage(`{"name":"new-node","run_list":[]}`)
	if err := c.Post(context.Background(), c.OrgPath("/nodes"), body, &created); err != nil {
		t.Fatal(err)
	}
	if created["uri"] == "" {
		t.Fatalf("expected uri in %v", created)
	}

	// Wrong key: rejected.
	other := s.AddClient("src", "other", false)
	bad, _ := chef.NewClient(chef.Config{ServerURL: s.URL("src"), ClientName: "tester", PrivateKeyPEM: other, HTTPClient: s.HTTP.Client()})
	err = bad.Get(context.Background(), bad.OrgPath("/nodes"), nil)
	var ae *chef.APIError
	if !asAPIError(err, &ae) || ae.Status != 401 {
		t.Fatalf("expected 401, got %v", err)
	}
}

func TestNotFoundAndRetry(t *testing.T) {
	s := chefserver.New()
	defer s.Close()
	s.Seed("src")
	key := s.AddClient("src", "tester", true)
	c, _ := chef.NewClient(chef.Config{ServerURL: s.URL("src"), ClientName: "tester", PrivateKeyPEM: key, HTTPClient: s.HTTP.Client()})
	err := c.Get(context.Background(), c.OrgPath("/nodes/nope"), nil)
	if !chef.IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}

	var calls int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer flaky.Close()
	fc, err := chef.NewClient(chef.Config{ServerURL: flaky.URL + "/organizations/x", ClientName: "t", PrivateKeyPEM: s.AddClient("src", "t2", false), HTTPClient: flaky.Client(), Retries: 3})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]bool
	if err := fc.Get(context.Background(), "/anything", &out); err != nil || !out["ok"] {
		t.Fatalf("expected success after retries, got %v %v", out, err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestKeyIsZeroedAfterParse(t *testing.T) {
	s := chefserver.New()
	defer s.Close()
	s.AddOrg("o")
	key := s.AddClient("o", "c", false)
	if _, err := chef.NewClient(chef.Config{ServerURL: s.URL("o"), ClientName: "c", PrivateKeyPEM: key}); err != nil {
		t.Fatal(err)
	}
	for _, b := range key {
		if b != 0 {
			t.Fatal("private key bytes were not zeroed")
		}
	}
}

func asAPIError(err error, target **chef.APIError) bool {
	if e, ok := err.(*chef.APIError); ok {
		*target = e
		return true
	}
	return false
}
