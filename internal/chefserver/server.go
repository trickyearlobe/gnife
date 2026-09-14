// Package chefserver is a Chef Infra Server implementation in Go. It backs
// gnife's tests in memory and "gnife serve" on disk: it verifies request
// signatures (protocol versions 1.0 to 1.3), serves the object, sandbox,
// bookshelf, depsolver, search, ACL, user and organisation endpoints that
// knife, chef-client and gnife use, and keeps every organisation's state in
// plain maps. A Store, when set, is told about every change.
package chefserver

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- signing protocols 1.0-1.2 are SHA-1 by definition
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickyearlobe/gnife/internal/kinds"
)

// MaxAPIVersion is the highest X-Ops-Server-API-Version served.
const MaxAPIVersion = 2

// Org is one organisation's state.
type Org struct {
	Name         string
	FullName     string
	Nodes        map[string]json.RawMessage
	Roles        map[string]json.RawMessage
	Environments map[string]json.RawMessage
	Clients      map[string]json.RawMessage // name -> {name, validator, ...}
	ClientKeys   map[string]map[string]string
	Groups       map[string]*Group
	Containers   map[string]bool
	DataBags     map[string]map[string]json.RawMessage
	Cookbooks    map[string]map[string]json.RawMessage // name -> version -> manifest (no urls)
	Artifacts    map[string]map[string]json.RawMessage
	Policies     map[string]map[string]json.RawMessage
	PolicyGroups map[string]map[string]string // group -> policy -> revision
	ACLs         map[string]map[string]Perm   // "kind/name" or "organization" -> perm
	Members      []string
	Invitations  map[string]string // id -> username
	Frozen       map[string]bool   // "cookbooks/name/version"
}

// Group is a group's membership.
type Group struct {
	Users   []string `json:"users"`
	Clients []string `json:"clients"`
	Groups  []string `json:"groups"`
}

// Perm is one permission of an ACL.
type Perm struct {
	Actors []string `json:"actors"`
	Groups []string `json:"groups"`
}

// User is a server-level user.
type User struct {
	Body      json.RawMessage
	PublicKey string
	Keys      map[string]string // extra named keys
	ACL       map[string]Perm
}

// Store is told about every change so it can persist it. Keys for ACLs are
// "organization" or "<plural>/<name>". Any method may be a no-op.
type Store interface {
	PutObject(org string, k *kinds.Kind, id kinds.ID, body json.RawMessage) error
	DeleteObject(org string, k *kinds.Kind, id kinds.ID) error
	PutCookbook(org string, k *kinds.Kind, id kinds.ID, manifest json.RawMessage, files map[string][]byte) error
	PutACL(org, key string, acl map[string]Perm) error
	PutUser(name string, body json.RawMessage, acl map[string]Perm) error
	DeleteUser(name string) error
	PutOrg(name, fullName string, members []string) error
	DeleteOrg(name string) error
}

// Server is the Chef server.
type Server struct {
	// HTTP is set by New (tests); NewServer leaves it nil and callers use Handler.
	HTTP *httptest.Server
	mu   sync.Mutex

	Orgs      map[string]*Org
	Users     map[string]*User
	Bookshelf map[string][]byte
	sandboxes map[string]map[string]bool
	// Superusers may list/create users and organisations.
	Superusers map[string]bool
	// Requests counts calls by "METHOD path" (tests).
	Requests map[string]int
	// Version is reported by /version and X-Ops-API-Info.
	Version string
	// Logf, if set, receives one line per request.
	Logf func(format string, args ...any)

	store Store
	base  string // base URL of the current request; valid while mu is held
}

// NewServer creates a server with no listener; serve Handler() yourself.
func NewServer(store Store) *Server {
	return &Server{
		Orgs: map[string]*Org{}, Users: map[string]*User{}, Bookshelf: map[string][]byte{},
		sandboxes: map[string]map[string]bool{}, Superusers: map[string]bool{"pivotal": true},
		Requests: map[string]int{}, Version: "dev", store: store,
	}
}

// New starts an in-memory server on a test listener.
func New() *Server {
	s := NewServer(nil)
	s.HTTP = httptest.NewServer(s.Handler())
	return s
}

// Close stops the test listener.
func (s *Server) Close() {
	if s.HTTP != nil {
		s.HTTP.Close()
	}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handle) }

// URL returns the organisation URL on the test listener.
func (s *Server) URL(org string) string { return s.HTTP.URL + "/organizations/" + org }

// ---------------------------------------------------------------------------
// State setters (used by stores when loading, and by tests)
// ---------------------------------------------------------------------------

// AddOrg creates an organisation with the standard groups and containers.
func (s *Server) AddOrg(name string) *Org {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newOrg(name, name)
}

func (s *Server) newOrg(name, full string) *Org {
	if o, ok := s.Orgs[name]; ok {
		return o
	}
	o := &Org{
		Name: name, FullName: full,
		Nodes: map[string]json.RawMessage{}, Roles: map[string]json.RawMessage{},
		Environments: map[string]json.RawMessage{"_default": json.RawMessage(`{"name":"_default","description":"The default Chef environment","cookbook_versions":{},"json_class":"Chef::Environment","chef_type":"environment","default_attributes":{},"override_attributes":{}}`)},
		Clients:      map[string]json.RawMessage{}, ClientKeys: map[string]map[string]string{},
		Groups: map[string]*Group{}, Containers: map[string]bool{},
		DataBags:  map[string]map[string]json.RawMessage{},
		Cookbooks: map[string]map[string]json.RawMessage{}, Artifacts: map[string]map[string]json.RawMessage{},
		Policies: map[string]map[string]json.RawMessage{}, PolicyGroups: map[string]map[string]string{},
		ACLs: map[string]map[string]Perm{}, Invitations: map[string]string{}, Frozen: map[string]bool{},
	}
	for _, g := range []string{"admins", "billing-admins", "clients", "users", "public_key_read_access"} {
		o.Groups[g] = &Group{Users: []string{}, Clients: []string{}, Groups: []string{}}
	}
	o.Groups["admins"].Users = []string{"pivotal"}
	for _, c := range []string{"clients", "containers", "cookbook_artifacts", "cookbooks", "data", "environments", "groups", "nodes", "policies", "policy_groups", "roles", "sandboxes"} {
		o.Containers[c] = true
	}
	s.Orgs[name] = o
	return o
}

// SetOrg creates or updates an organisation record without persisting.
func (s *Server) SetOrg(name, fullName string, members []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.newOrg(name, fullName)
	o.FullName = fullName
	o.Members = append([]string(nil), members...)
}

// SetObject stores an object in the layout the API returns, without
// persisting. Bodies are the on-disk (knife-ec-backup) forms.
func (s *Server) SetObject(org string, k *kinds.Kind, id kinds.ID, body json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.newOrg(org, org)
	return o.set(k, id, body)
}

func (o *Org) set(k *kinds.Kind, id kinds.ID, body json.RawMessage) error {
	switch k {
	case kinds.Node:
		o.Nodes[id.Name] = body
	case kinds.Role:
		o.Roles[id.Name] = body
	case kinds.Environment:
		o.Environments[id.Name] = body
	case kinds.Container:
		o.Containers[id.Name] = true
	case kinds.Client:
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return err
		}
		pub, _ := m["public_key"].(string)
		delete(m, "public_key")
		delete(m, "private_key")
		m["name"] = id.Name
		m["clientname"] = id.Name
		m["orgname"] = o.Name
		if _, ok := m["validator"]; !ok {
			m["validator"] = false
		}
		m["json_class"] = "Chef::ApiClient"
		m["chef_type"] = "client"
		o.Clients[id.Name], _ = json.Marshal(m)
		if o.ClientKeys[id.Name] == nil {
			o.ClientKeys[id.Name] = map[string]string{}
		}
		if pub != "" {
			o.ClientKeys[id.Name]["default"] = pub
		}
	case kinds.Group:
		users, clients, groups := kinds.GroupMembers(body)
		o.Groups[id.Name] = &Group{Users: users, Clients: clients, Groups: groups}
	case kinds.DataBag:
		if o.DataBags[id.Name] == nil {
			o.DataBags[id.Name] = map[string]json.RawMessage{}
		}
	case kinds.DataBagItem:
		if o.DataBags[id.Name] == nil {
			o.DataBags[id.Name] = map[string]json.RawMessage{}
		}
		o.DataBags[id.Name][id.Sub] = body
	case kinds.Policy:
		if o.Policies[id.Name] == nil {
			o.Policies[id.Name] = map[string]json.RawMessage{}
		}
		o.Policies[id.Name][id.Sub] = body
	case kinds.PolicyGroup:
		o.PolicyGroups[id.Name] = kinds.PolicyGroupPolicies(body)
	default:
		return fmt.Errorf("chefserver: cannot set %s objects", k.Name)
	}
	return nil
}

// SetCookbook stores a cookbook or artifact version and its files without
// persisting. The manifest may carry URLs; they are dropped.
func (s *Server) SetCookbook(org string, k *kinds.Kind, id kinds.ID, manifest json.RawMessage, files map[string][]byte, frozen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.newOrg(org, org)
	for sum, data := range files {
		s.Bookshelf[sum] = data
	}
	store := o.Cookbooks
	if k == kinds.Artifact {
		store = o.Artifacts
	}
	if store[id.Name] == nil {
		store[id.Name] = map[string]json.RawMessage{}
	}
	var mf map[string]any
	_ = json.Unmarshal(stripURLs(manifest), &mf)
	mf["frozen?"] = frozen
	store[id.Name][id.Sub], _ = json.Marshal(mf)
	if frozen {
		o.Frozen[k.Plural+"/"+id.Name+"/"+id.Sub] = true
	}
}

// SetACL stores an ACL without persisting; key is "organization" or "<plural>/<name>".
func (s *Server) SetACL(org, key string, acl map[string]Perm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.newOrg(org, org)
	o.ACLs[key] = acl
}

// SetUser stores a server-level user (body may carry public_key) without persisting.
func (s *Server) SetUser(name string, body json.RawMessage, acl map[string]Perm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	pub, _ := m["public_key"].(string)
	delete(m, "public_key")
	delete(m, "private_key")
	delete(m, "password")
	m["username"] = name
	raw, _ := json.Marshal(m)
	u := &User{Body: raw, PublicKey: pub, Keys: map[string]string{}, ACL: acl}
	if old, ok := s.Users[name]; ok && pub == "" {
		u.PublicKey = old.PublicKey
	}
	s.Users[name] = u
}

// GeneratePivotal creates the superuser with a fresh key and returns the
// private key PEM. Existing pivotal state is replaced.
func (s *Server) GeneratePivotal() ([]byte, error) {
	priv, pub, err := generateKeyPair()
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"username": "pivotal", "display_name": "Chef Server Superuser", "email": "root@localhost", "public_key": pub})
	s.SetUser("pivotal", body, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		if err := s.store.PutUser("pivotal", body, nil); err != nil {
			return nil, err
		}
	}
	return priv, nil
}

// HasUser reports whether a user exists.
func (s *Server) HasUser(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Users[name]
	return ok
}

// KeyPair generates an RSA key pair as (private PEM, public PEM).
func KeyPair() ([]byte, string, error) { return generateKeyPair() }

func generateKeyPair() (privPEM []byte, pubPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), nil
}

func stripURLs(manifest json.RawMessage) json.RawMessage {
	var mf map[string]any
	if json.Unmarshal(manifest, &mf) != nil {
		return manifest
	}
	if files, ok := mf["all_files"].([]any); ok {
		for _, f := range files {
			if fm, ok := f.(map[string]any); ok {
				delete(fm, "url")
			}
		}
	}
	out, _ := json.Marshal(mf)
	return out
}

// ---------------------------------------------------------------------------
// Persistence hooks (called with mu held)
// ---------------------------------------------------------------------------

func (s *Server) persistObject(o *Org, k *kinds.Kind, id kinds.ID, body json.RawMessage) *apiErr {
	if s.store == nil {
		return nil
	}
	if err := s.store.PutObject(o.Name, k, id, body); err != nil {
		return fail(500, "persisting %s %s: %v", k.Name, id, err)
	}
	return nil
}

func (s *Server) persistDelete(o *Org, k *kinds.Kind, id kinds.ID) *apiErr {
	if s.store == nil {
		return nil
	}
	if err := s.store.DeleteObject(o.Name, k, id); err != nil {
		return fail(500, "persisting delete of %s %s: %v", k.Name, id, err)
	}
	return nil
}

func (s *Server) persistClient(o *Org, name string) *apiErr {
	var m map[string]any
	_ = json.Unmarshal(o.Clients[name], &m)
	if pub := o.ClientKeys[name]["default"]; pub != "" {
		m["public_key"] = pub
	}
	body, _ := json.Marshal(m)
	return s.persistObject(o, kinds.Client, kinds.ID{Name: name}, body)
}

func (s *Server) persistGroup(o *Org, name string) *apiErr {
	g := o.Groups[name]
	body, _ := json.Marshal(map[string]any{"name": name, "groupname": name, "users": g.Users, "clients": g.Clients, "groups": g.Groups})
	return s.persistObject(o, kinds.Group, kinds.ID{Name: name}, body)
}

func (s *Server) persistPolicyGroup(o *Org, name string) *apiErr {
	pm := map[string]any{}
	for p, r := range o.PolicyGroups[name] {
		pm[p] = map[string]string{"revision_id": r}
	}
	body, _ := json.Marshal(map[string]any{"policies": pm})
	return s.persistObject(o, kinds.PolicyGroup, kinds.ID{Name: name}, body)
}

func (s *Server) persistCookbook(o *Org, k *kinds.Kind, id kinds.ID, manifest json.RawMessage) *apiErr {
	if s.store == nil {
		return nil
	}
	var mf struct {
		Files []struct {
			Checksum string `json:"checksum"`
		} `json:"all_files"`
	}
	_ = json.Unmarshal(manifest, &mf)
	files := map[string][]byte{}
	for _, f := range mf.Files {
		files[f.Checksum] = s.Bookshelf[f.Checksum]
	}
	if err := s.store.PutCookbook(o.Name, k, id, manifest, files); err != nil {
		return fail(500, "persisting %s %s: %v", k.Name, id, err)
	}
	return nil
}

func (s *Server) persistACL(o *Org, key string) *apiErr {
	if s.store == nil {
		return nil
	}
	if err := s.store.PutACL(o.Name, key, o.ACLs[key]); err != nil {
		return fail(500, "persisting acl %s: %v", key, err)
	}
	return nil
}

func (s *Server) persistUser(name string) *apiErr {
	if s.store == nil {
		return nil
	}
	u, ok := s.Users[name]
	if !ok {
		if err := s.store.DeleteUser(name); err != nil {
			return fail(500, "persisting delete of user %s: %v", name, err)
		}
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(u.Body, &m)
	if u.PublicKey != "" {
		m["public_key"] = u.PublicKey
	}
	body, _ := json.Marshal(m)
	if err := s.store.PutUser(name, body, u.ACL); err != nil {
		return fail(500, "persisting user %s: %v", name, err)
	}
	return nil
}

func (s *Server) persistOrg(o *Org) *apiErr {
	if s.store == nil {
		return nil
	}
	if err := s.store.PutOrg(o.Name, o.FullName, o.Members); err != nil {
		return fail(500, "persisting organization %s: %v", o.Name, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP entry point, authentication
// ---------------------------------------------------------------------------

type apiErr struct {
	code int
	msg  string
	body any // optional structured body
}

func (e *apiErr) Error() string { return e.msg }

func fail(code int, format string, args ...any) *apiErr {
	return &apiErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// request bundles what routes need.
type request struct {
	r       *http.Request
	body    []byte
	user    string
	super   bool
	version int
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	start := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests[r.Method+" "+r.URL.Path]++
	s.base = baseURL(r)

	code, out, err := s.dispatch(w, r, body)
	if err != nil {
		code = err.code
		if err.body != nil {
			writeJSON(w, code, err.body)
		} else {
			writeJSON(w, code, map[string]any{"error": []string{err.msg}})
		}
	} else if out != nil {
		writeJSON(w, code, out)
	}
	if s.Logf != nil {
		s.Logf("%s %s %d %s api=%s ua=%q", r.Method, r.URL.RequestURI(), code, time.Since(start).Round(time.Millisecond),
			r.Header.Get("X-Ops-Server-API-Version"), r.Header.Get("User-Agent"))
	}
}

// handled marks responses written directly (bookshelf bodies, /version).
var handled = &apiErr{}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, body []byte) (int, any, *apiErr) {
	// Unauthenticated endpoints.
	switch {
	case strings.HasPrefix(r.URL.Path, "/bookshelf/"):
		return s.bookshelf(w, r, body)
	case r.URL.Path == "/version":
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		fmt.Fprintf(w, "gnife serve %s\n", s.Version)
		return 200, nil, nil
	case r.URL.Path == "/_status":
		return 200, map[string]any{"status": "pong", "upstreams": map[string]string{}, "keygen": map[string]any{}}, nil
	}

	version, aerr := apiVersion(r)
	if aerr != nil {
		return 0, nil, aerr
	}
	// Chef clients negotiate from this header; without it they assume an
	// unversioned (v0) server and read cookbook manifests as segments.
	w.Header().Set("X-Ops-Server-API-Version", fmt.Sprintf(`{"min_version":"0","max_version":"%d","request_version":"%d","response_version":"%d"}`, MaxAPIVersion, version, version))
	w.Header().Set("X-Ops-API-Info", fmt.Sprintf("flavor=cs;version=%d;gnife=%s", version, s.Version))

	user, aerr := s.authenticate(r, body)
	if aerr != nil {
		return 0, nil, aerr
	}
	req := &request{r: r, body: body, user: user, super: s.Superusers[user], version: version}
	return s.route(req)
}

func apiVersion(r *http.Request) (int, *apiErr) {
	h := r.Header.Get("X-Ops-Server-API-Version")
	if h == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(h)
	if err != nil || v < 0 || v > MaxAPIVersion {
		return 0, &apiErr{code: 406, msg: "invalid-x-ops-server-api-version", body: map[string]any{
			"error": "invalid-x-ops-server-api-version", "message": "Specified version " + h + " not supported",
			"min_version": 0, "max_version": MaxAPIVersion,
		}}
	}
	return v, nil
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) bookshelf(w http.ResponseWriter, r *http.Request, body []byte) (int, any, *apiErr) {
	sum := strings.TrimPrefix(r.URL.Path, "/bookshelf/")
	switch r.Method {
	case http.MethodGet:
		data, ok := s.Bookshelf[sum]
		if !ok {
			w.WriteHeader(404)
			return 404, nil, nil
		}
		w.Header().Set("Content-Type", "application/x-binary")
		w.WriteHeader(200)
		_, _ = w.Write(data)
		return 200, nil, nil
	case http.MethodPut:
		if checksum(body) != sum {
			w.WriteHeader(400)
			return 400, nil, nil
		}
		s.Bookshelf[sum] = body
		w.WriteHeader(200)
		return 200, nil, nil
	}
	w.WriteHeader(405)
	return 405, nil, nil
}

// authenticate verifies the request signature and returns the caller's name.
func (s *Server) authenticate(r *http.Request, body []byte) (string, *apiErr) {
	name := r.Header.Get("X-Ops-Userid")
	if name == "" {
		return "", fail(401, "'X-Ops-Userid' header is missing")
	}
	sign := r.Header.Get("X-Ops-Sign")
	sv := "1.0"
	for _, part := range strings.Split(sign, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == "version" {
			sv = v
		}
	}
	ts := r.Header.Get("X-Ops-Timestamp")
	t, err := time.Parse("2006-01-02T15:04:05Z", ts)
	if err != nil || time.Since(t).Abs() > 15*time.Minute {
		return "", fail(401, "Failed to authenticate as '%s'. Timestamp is invalid or clock skew is too large.", name)
	}
	var sigB64 strings.Builder
	for i := 1; ; i++ {
		seg := r.Header.Get(fmt.Sprintf("X-Ops-Authorization-%d", i))
		if seg == "" {
			break
		}
		sigB64.WriteString(seg)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64.String())
	if err != nil || len(sig) == 0 {
		return "", fail(401, "Failed to authenticate as '%s'. Signature is missing or malformed.", name)
	}
	pub := s.publicKeyFor(orgOf(r.URL.Path), name)
	if pub == nil {
		return "", fail(401, "Failed to authenticate as '%s'. Ensure that your node_name and client key are correct.", name)
	}

	path := r.URL.Path
	contentHash := r.Header.Get("X-Ops-Content-Hash")
	var canonical string
	var verify func() error
	switch sv {
	case "1.3":
		h := sha256.Sum256(body)
		if contentHash != base64.StdEncoding.EncodeToString(h[:]) {
			return "", fail(401, "Failed to authenticate as '%s'. Content hash mismatch.", name)
		}
		canonical = strings.Join([]string{
			"Method:" + r.Method, "Path:" + path, "X-Ops-Content-Hash:" + contentHash,
			"X-Ops-Sign:version=1.3", "X-Ops-Timestamp:" + ts, "X-Ops-UserId:" + name,
			"X-Ops-Server-API-Version:" + r.Header.Get("X-Ops-Server-Api-Version"),
		}, "\n")
		verify = func() error {
			d := sha256.Sum256([]byte(canonical))
			return rsa.VerifyPKCS1v15(pub, crypto.SHA256, d[:], sig)
		}
	case "1.0", "1.1", "1.2":
		h := sha1.Sum(body) // #nosec G401
		if contentHash != base64.StdEncoding.EncodeToString(h[:]) {
			return "", fail(401, "Failed to authenticate as '%s'. Content hash mismatch.", name)
		}
		ph := sha1.Sum([]byte(path)) // #nosec G401
		userID := name
		if sv != "1.0" {
			uh := sha1.Sum([]byte(name)) // #nosec G401
			userID = base64.StdEncoding.EncodeToString(uh[:])
		}
		canonical = strings.Join([]string{
			"Method:" + r.Method, "Hashed Path:" + base64.StdEncoding.EncodeToString(ph[:]),
			"X-Ops-Content-Hash:" + contentHash, "X-Ops-Timestamp:" + ts, "X-Ops-UserId:" + userID,
		}, "\n")
		if sv == "1.2" {
			verify = func() error {
				d := sha1.Sum([]byte(canonical)) // #nosec G401
				return rsa.VerifyPKCS1v15(pub, crypto.SHA1, d[:], sig)
			}
		} else {
			// 1.0/1.1: the canonical string is "encrypted" with the private
			// key, i.e. a PKCS#1 v1.5 signature over the raw bytes.
			verify = func() error { return rsa.VerifyPKCS1v15(pub, 0, []byte(canonical), sig) }
		}
	default:
		return "", fail(401, "Failed to authenticate as '%s'. Unsupported signing version %s.", name, sv)
	}
	if err := verify(); err != nil {
		return "", fail(401, "Failed to authenticate as '%s'. Ensure that your node_name and client key are correct.", name)
	}
	return name, nil
}

// publicKeyFor finds the key to verify with: an org client's for org paths,
// else a user's.
func (s *Server) publicKeyFor(org, name string) *rsa.PublicKey {
	var pems []string
	if org != "" {
		if o, ok := s.Orgs[org]; ok {
			if keys, ok := o.ClientKeys[name]; ok {
				for _, k := range keys {
					pems = append(pems, k)
				}
			}
		}
	}
	if u, ok := s.Users[name]; ok {
		pems = append(pems, u.PublicKey)
		for _, k := range u.Keys {
			pems = append(pems, k)
		}
	}
	for _, p := range pems {
		if pub := parsePublicKey(p); pub != nil {
			return pub
		}
	}
	return nil
}

func parsePublicKey(pemStr string) *rsa.PublicKey {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil
	}
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk
		}
	}
	if k, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return k
	}
	return nil
}

func orgOf(path string) string {
	if !strings.HasPrefix(path, "/organizations/") {
		return ""
	}
	rest := strings.TrimPrefix(path, "/organizations/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func nameOf(body []byte, keys ...string) string {
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
