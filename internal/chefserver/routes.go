package chefserver

import (
	"crypto/md5" // #nosec G501 -- Chef identifies cookbook files by MD5
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/trickyearlobe/gnife/internal/deps"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

func checksum(data []byte) string {
	sum := md5.Sum(data) // #nosec G401
	return hex.EncodeToString(sum[:])
}

// Checksum is the hex MD5 the server keys bookshelf content by.
func Checksum(data []byte) string { return checksum(data) }

func (s *Server) orgURL(org string) string { return s.base + "/organizations/" + org }

// route dispatches an authenticated request.
func (s *Server) route(req *request) (int, any, *apiErr) {
	r := req.r
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	m := r.Method
	if m == http.MethodHead {
		// HEAD is GET without a body; cobra-style existence checks.
		req.r.Method = http.MethodGet
		code, _, err := s.route(req)
		req.r.Method = http.MethodHead
		return code, nil, err
	}

	switch parts[0] {
	case "users":
		return s.routeUsers(req, parts[1:])
	case "license":
		return 200, map[string]any{"limit_exceeded": false, "node_license": 25, "node_count": 0, "upstream_license": 0}, nil
	case "_stats":
		return 200, []any{}, nil
	case "organizations":
		if len(parts) == 1 {
			switch m {
			case http.MethodGet:
				if !req.super {
					return 0, nil, fail(403, "missing read permission")
				}
				out := map[string]string{}
				for n := range s.Orgs {
					out[n] = s.orgURL(n)
				}
				return 200, out, nil
			case http.MethodPost:
				if !req.super {
					return 0, nil, fail(403, "missing create permission")
				}
				var raw map[string]string
				_ = json.Unmarshal(req.body, &raw)
				name, full := raw["name"], raw["full_name"]
				if name == "" {
					return 0, nil, fail(400, "Field 'name' missing")
				}
				if _, ok := s.Orgs[name]; ok {
					return 0, nil, fail(409, "Organization already exists")
				}
				o := s.newOrg(name, full)
				if err := s.persistOrg(o); err != nil {
					return 0, nil, err
				}
				// The validator client is created with the organisation.
				vname := name + "-validator"
				priv, pub, err := generateKeyPair()
				if err != nil {
					return 0, nil, fail(500, "generating validator key: %v", err)
				}
				o.Clients[vname], _ = json.Marshal(map[string]any{"name": vname, "clientname": vname, "validator": true, "orgname": name, "json_class": "Chef::ApiClient", "chef_type": "client"})
				o.ClientKeys[vname] = map[string]string{"default": pub}
				if err := s.persistClient(o, vname); err != nil {
					return 0, nil, err
				}
				return 201, map[string]string{"uri": s.orgURL(name), "clientname": vname, "private_key": string(priv)}, nil
			}
		}
		o, ok := s.Orgs[parts[1]]
		if !ok {
			return 0, nil, fail(404, "organization '%s' does not exist", parts[1])
		}
		if len(parts) == 2 {
			switch m {
			case http.MethodGet:
				return 200, map[string]string{"name": o.Name, "full_name": o.FullName, "guid": fmt.Sprintf("%032x", 0)}, nil
			case http.MethodPut:
				if !req.super {
					return 0, nil, fail(403, "missing update permission")
				}
				var raw map[string]string
				_ = json.Unmarshal(req.body, &raw)
				if raw["full_name"] != "" {
					o.FullName = raw["full_name"]
				}
				if err := s.persistOrg(o); err != nil {
					return 0, nil, err
				}
				return 200, map[string]string{"name": o.Name, "full_name": o.FullName}, nil
			case http.MethodDelete:
				if !req.super {
					return 0, nil, fail(403, "missing delete permission")
				}
				delete(s.Orgs, parts[1])
				if s.store != nil {
					if err := s.store.DeleteOrg(parts[1]); err != nil {
						return 0, nil, fail(500, "persisting delete: %v", err)
					}
				}
				return 200, nil, nil
			}
		}
		return s.routeOrg(req, o, parts[2:])
	}
	return 0, nil, fail(404, "no route for %s", r.URL.Path)
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *Server) routeUsers(req *request, parts []string) (int, any, *apiErr) {
	m := req.r.Method
	if len(parts) == 0 {
		switch m {
		case http.MethodGet:
			if !req.super {
				return 0, nil, fail(403, "missing read permission")
			}
			out := map[string]string{}
			for n := range s.Users {
				out[n] = s.base + "/users/" + n
			}
			return 200, out, nil
		case http.MethodPost:
			if !req.super {
				return 0, nil, fail(403, "missing create permission")
			}
			var in map[string]any
			_ = json.Unmarshal(req.body, &in)
			name, _ := in["username"].(string)
			if name == "" {
				return 0, nil, fail(400, "Field 'username' missing")
			}
			if _, ok := s.Users[name]; ok {
				return 0, nil, fail(409, "Username already taken")
			}
			pub, _ := in["public_key"].(string)
			createKey, _ := in["create_key"].(bool)
			var priv []byte
			if pub == "" && createKey {
				var err error
				priv, pub, err = generateKeyPair()
				if err != nil {
					return 0, nil, fail(500, "generating key: %v", err)
				}
			}
			delete(in, "public_key")
			delete(in, "password")
			delete(in, "create_key")
			raw, _ := json.Marshal(in)
			s.Users[name] = &User{Body: raw, PublicKey: pub, Keys: map[string]string{}}
			if err := s.persistUser(name); err != nil {
				return 0, nil, err
			}
			out := map[string]any{"uri": s.base + "/users/" + name}
			if priv != nil {
				out["private_key"] = string(priv)
				out["chef_key"] = map[string]any{"name": "default", "public_key": pub, "private_key": string(priv), "expiration_date": "infinity", "uri": s.base + "/users/" + name + "/keys/default"}
			}
			return 201, out, nil
		}
	}
	u, ok := s.Users[parts[0]]
	if !ok {
		return 0, nil, fail(404, "user '%s' not found", parts[0])
	}
	if len(parts) == 1 {
		switch m {
		case http.MethodGet:
			if req.version == 0 {
				var mm map[string]any
				_ = json.Unmarshal(u.Body, &mm)
				mm["public_key"] = u.PublicKey
				return 200, mm, nil
			}
			return 200, u.Body, nil
		case http.MethodPut:
			if !req.super && req.user != parts[0] {
				return 0, nil, fail(403, "missing update permission")
			}
			var in map[string]any
			_ = json.Unmarshal(req.body, &in)
			if pub, _ := in["public_key"].(string); pub != "" && req.version == 0 {
				u.PublicKey = pub
			}
			delete(in, "public_key")
			delete(in, "password")
			delete(in, "private_key")
			in["username"] = parts[0]
			u.Body, _ = json.Marshal(in)
			if err := s.persistUser(parts[0]); err != nil {
				return 0, nil, err
			}
			return 200, u.Body, nil
		case http.MethodDelete:
			if !req.super {
				return 0, nil, fail(403, "missing delete permission")
			}
			delete(s.Users, parts[0])
			if err := s.persistUser(parts[0]); err != nil {
				return 0, nil, err
			}
			return 200, u.Body, nil
		}
	}
	switch parts[1] {
	case "keys":
		all := map[string]string{"default": u.PublicKey}
		for k, v := range u.Keys {
			all[k] = v
		}
		return s.routeKeys(req, all, func(k map[string]string) *apiErr {
			u.PublicKey = k["default"]
			u.Keys = map[string]string{}
			for n, v := range k {
				if n != "default" {
					u.Keys[n] = v
				}
			}
			return s.persistUser(parts[0])
		}, parts[2:])
	case "_acl":
		if u.ACL == nil {
			u.ACL = defaultACL([]string{parts[0]})
		}
		return s.routeACL(req, u.ACL, parts[2:], func() *apiErr { return s.persistUser(parts[0]) })
	case "association_requests":
		// PUT /users/U/association_requests/ID {"response":"accept"}
		if len(parts) == 3 && m == http.MethodPut {
			for _, o := range s.Orgs {
				if user, ok := o.Invitations[parts[2]]; ok && user == parts[0] {
					delete(o.Invitations, parts[2])
					var resp struct {
						Response string `json:"response"`
					}
					_ = json.Unmarshal(req.body, &resp)
					if resp.Response == "accept" {
						o.Members = appendUnique(o.Members, user)
						if err := s.persistOrg(o); err != nil {
							return 0, nil, err
						}
					}
					return 200, map[string]any{"organization": map[string]string{"name": o.Name}}, nil
				}
			}
			return 0, nil, fail(404, "association request not found")
		}
		if m == http.MethodGet {
			var out []map[string]string
			for _, o := range s.Orgs {
				for id, user := range o.Invitations {
					if user == parts[0] {
						out = append(out, map[string]string{"id": id, "orgname": o.Name})
					}
				}
			}
			if out == nil {
				out = []map[string]string{}
			}
			return 200, out, nil
		}
	case "organizations":
		var out []map[string]map[string]string
		for _, o := range s.Orgs {
			for _, u := range o.Members {
				if u == parts[0] {
					out = append(out, map[string]map[string]string{"organization": {"name": o.Name, "full_name": o.FullName}})
				}
			}
		}
		if out == nil {
			out = []map[string]map[string]string{}
		}
		return 200, out, nil
	}
	return 0, nil, fail(404, "no route")
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// routeKeys serves /keys[/NAME] for clients and users. keys is the current
// set; save is called with the new set.
func (s *Server) routeKeys(req *request, keys map[string]string, save func(map[string]string) *apiErr, parts []string) (int, any, *apiErr) {
	m := req.r.Method
	if len(parts) == 0 {
		switch m {
		case http.MethodGet:
			out := []map[string]any{}
			for _, n := range sortedKeys(keys) {
				out = append(out, map[string]any{"name": n, "uri": req.r.URL.Path + "/" + n, "expired": false})
			}
			return 200, out, nil
		case http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(req.body, &in)
			name, _ := in["name"].(string)
			if name == "" {
				return 0, nil, fail(400, "Field 'name' missing")
			}
			if _, ok := keys[name]; ok {
				return 0, nil, fail(409, "Key named '%s' already exists", name)
			}
			pub, _ := in["public_key"].(string)
			createKey, _ := in["create_key"].(bool)
			var priv []byte
			if pub == "" && createKey {
				var err error
				priv, pub, err = generateKeyPair()
				if err != nil {
					return 0, nil, fail(500, "generating key: %v", err)
				}
			}
			if pub == "" {
				return 0, nil, fail(400, "Field 'public_key' missing (or create_key)")
			}
			keys[name] = pub
			if err := save(keys); err != nil {
				return 0, nil, err
			}
			out := map[string]any{"uri": s.base + req.r.URL.Path + "/" + name, "public_key": pub, "expiration_date": in["expiration_date"]}
			if priv != nil {
				out["private_key"] = string(priv)
			}
			return 201, out, nil
		}
	}
	name := parts[0]
	pub, ok := keys[name]
	if !ok {
		return 0, nil, fail(404, "key '%s' not found", name)
	}
	switch m {
	case http.MethodGet:
		return 200, map[string]any{"name": name, "public_key": pub, "expiration_date": "infinity"}, nil
	case http.MethodPut:
		var in map[string]any
		_ = json.Unmarshal(req.body, &in)
		if p, _ := in["public_key"].(string); p != "" {
			keys[name] = p
		}
		if err := save(keys); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"name": name, "public_key": keys[name], "expiration_date": "infinity"}, nil
	case http.MethodDelete:
		delete(keys, name)
		if err := save(keys); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"name": name, "public_key": pub, "expiration_date": "infinity"}, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// ACLs
// ---------------------------------------------------------------------------

func defaultACL(actors []string) map[string]Perm {
	a := map[string]Perm{}
	for _, p := range []string{"create", "read", "update", "delete", "grant"} {
		a[p] = Perm{Actors: append([]string{"pivotal"}, actors...), Groups: []string{"admins"}}
	}
	return a
}

// aclOf returns (creating if needed) the ACL for key.
func (o *Org) aclOf(key string, defaultActors []string) map[string]Perm {
	a, ok := o.ACLs[key]
	if !ok {
		a = defaultACL(defaultActors)
		if strings.HasPrefix(key, "containers/") || key == "organization" || strings.HasPrefix(key, "groups/") {
			// Members read organisation-level things.
			r := a["read"]
			r.Groups = append(r.Groups, "users")
			a["read"] = r
		}
		o.ACLs[key] = a
	}
	return a
}

func (s *Server) routeACL(req *request, a map[string]Perm, parts []string, persist func() *apiErr) (int, any, *apiErr) {
	m := req.r.Method
	render := func(p Perm) map[string]any {
		out := map[string]any{"actors": nonNil(p.Actors), "groups": nonNil(p.Groups)}
		if req.version >= 1 {
			users, clients := []string{}, []string{}
			for _, actor := range p.Actors {
				if _, ok := s.Users[actor]; ok || actor == "pivotal" {
					users = append(users, actor)
				} else {
					clients = append(clients, actor)
				}
			}
			out["users"], out["clients"] = users, clients
		}
		return out
	}
	if len(parts) == 0 {
		if m != http.MethodGet {
			return 0, nil, fail(405, "method not allowed")
		}
		out := map[string]any{}
		for perm, p := range a {
			out[perm] = render(p)
		}
		return 200, out, nil
	}
	perm := parts[0]
	if _, ok := a[perm]; !ok {
		return 0, nil, fail(404, "no such permission '%s'", perm)
	}
	if m != http.MethodPut {
		return 0, nil, fail(405, "method not allowed")
	}
	var in map[string]struct {
		Actors  []string `json:"actors"`
		Groups  []string `json:"groups"`
		Users   []string `json:"users"`
		Clients []string `json:"clients"`
	}
	if err := json.Unmarshal(req.body, &in); err != nil {
		return 0, nil, fail(400, "invalid body")
	}
	p, ok := in[perm]
	if !ok {
		return 0, nil, fail(400, "body must contain '%s'", perm)
	}
	actors := p.Actors
	if p.Users != nil || p.Clients != nil {
		if p.Actors != nil {
			return 0, nil, fail(400, "'actors' and 'users'/'clients' are mutually exclusive")
		}
		actors = append(append([]string{}, p.Users...), p.Clients...)
	}
	a[perm] = Perm{Actors: nonNil(actors), Groups: nonNil(p.Groups)}
	if err := persist(); err != nil {
		return 0, nil, err
	}
	return 200, map[string]any{perm: render(a[perm])}, nil
}

// ---------------------------------------------------------------------------
// Organisation-scoped routes
// ---------------------------------------------------------------------------

func (s *Server) routeOrg(req *request, o *Org, parts []string) (int, any, *apiErr) {
	if len(parts) == 0 {
		return 0, nil, fail(404, "no route")
	}
	m := req.r.Method
	col := parts[0]
	rest := parts[1:]
	url := func(kind, name string) string { return s.orgURL(o.Name) + "/" + kind + "/" + name }
	objACL := func(k *kinds.Kind, name string, defaultActors []string) (int, any, *apiErr) {
		key := k.Plural + "/" + name
		return s.routeACL(req, o.aclOf(key, defaultActors), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}

	// Plain JSON collections.
	simple := map[string]*kinds.Kind{"nodes": kinds.Node, "roles": kinds.Role, "environments": kinds.Environment}
	if k, ok := simple[col]; ok {
		store := map[*kinds.Kind]map[string]json.RawMessage{kinds.Node: o.Nodes, kinds.Role: o.Roles, kinds.Environment: o.Environments}[k]
		if len(rest) >= 2 {
			if _, ok := store[rest[0]]; !ok {
				return 0, nil, fail(404, "%s '%s' not found", k.Name, rest[0])
			}
			switch {
			case rest[1] == "_acl":
				return objACL(k, rest[0], nil)
			case k == kinds.Environment && rest[1] == "cookbook_versions" && m == http.MethodPost:
				return s.depsolve(o, rest[0], req.body, req.version)
			case k == kinds.Environment && rest[1] == "cookbooks":
				return s.envCookbooks(o, rest[0], rest[2:], req)
			case k == kinds.Environment && rest[1] == "recipes":
				return 200, s.recipes(o, rest[0]), nil
			case k == kinds.Environment && rest[1] == "nodes":
				return 200, s.inEnvironment(o, o.Nodes, rest[0], "nodes", url), nil
			case k == kinds.Environment && rest[1] == "roles":
				return 200, s.inEnvironment(o, o.Roles, rest[0], "roles", url), nil
			case k == kinds.Role && rest[1] == "environments":
				return s.roleEnvironments(o, rest[0], rest[2:])
			}
		}
		return s.crud(o, k, store, rest, req, url)
	}

	switch col {
	case "users":
		return s.routeMembers(req, o, rest)
	case "association_requests":
		switch m {
		case http.MethodGet:
			out := []map[string]string{}
			for _, id := range sortedKeys(o.Invitations) {
				out = append(out, map[string]string{"id": id, "username": o.Invitations[id]})
			}
			return 200, out, nil
		case http.MethodPost:
			name := nameOf(req.body, "user")
			if _, ok := s.Users[name]; !ok {
				return 0, nil, fail(404, "user '%s' not found", name)
			}
			id := strconv.FormatInt(time.Now().UnixNano(), 36)
			o.Invitations[id] = name
			return 201, map[string]any{"uri": url("association_requests", id), "organization_user": map[string]string{"username": req.user}, "organization": map[string]string{"name": o.Name}, "user": map[string]string{"username": name}}, nil
		case http.MethodDelete:
			if len(rest) == 1 {
				delete(o.Invitations, rest[0])
				return 200, nil, nil
			}
		}
	case "organizations":
		if len(rest) >= 1 && rest[0] == "_acl" {
			rest = append([]string{"", ""}, rest[1:]...) // objACL slices rest[2:]
			return s.routeACL(req, o.aclOf("organization", nil), rest[2:], func() *apiErr { return s.persistACL(o, "organization") })
		}
	case "clients":
		return s.routeClients(req, o, rest, url)
	case "groups":
		return s.routeGroups(req, o, rest, url)
	case "containers":
		if len(rest) >= 2 && rest[1] == "_acl" {
			if !o.Containers[rest[0]] {
				return 0, nil, fail(404, "container '%s' not found", rest[0])
			}
			return objACL(kinds.Container, rest[0], nil)
		}
		if len(rest) == 0 {
			switch m {
			case http.MethodGet:
				out := map[string]string{}
				for n := range o.Containers {
					out[n] = url("containers", n)
				}
				return 200, out, nil
			case http.MethodPost:
				name := nameOf(req.body, "containername", "name")
				if name == "" {
					return 0, nil, fail(400, "Field 'containername' missing")
				}
				if o.Containers[name] {
					return 0, nil, fail(409, "Container already exists")
				}
				o.Containers[name] = true
				if err := s.persistObject(o, kinds.Container, kinds.ID{Name: name}, req.body); err != nil {
					return 0, nil, err
				}
				return 201, map[string]string{"uri": url("containers", name)}, nil
			}
		}
		if !o.Containers[rest[0]] {
			return 0, nil, fail(404, "container '%s' not found", rest[0])
		}
		switch m {
		case http.MethodGet:
			return 200, map[string]string{"containername": rest[0], "containerpath": rest[0]}, nil
		case http.MethodDelete:
			delete(o.Containers, rest[0])
			if err := s.persistDelete(o, kinds.Container, kinds.ID{Name: rest[0]}); err != nil {
				return 0, nil, err
			}
			return 200, nil, nil
		}
	case "data":
		return s.routeData(req, o, rest, url)
	case "cookbooks", "cookbook_artifacts":
		return s.routeCookbooks(req, o, col, rest, url)
	case "sandboxes":
		return s.routeSandboxes(req, rest)
	case "policies":
		return s.routePolicies(req, o, rest, url)
	case "policy_groups":
		return s.routePolicyGroups(req, o, rest, url)
	case "search":
		return s.routeSearch(req, o, rest, url)
	case "universe":
		return 200, s.universe(o), nil
	case "_validator_key", "required_recipe", "reports", "data-collector", "runs":
		return 0, nil, fail(404, "not found")
	}
	return 0, nil, fail(404, "no route for %s", req.r.URL.Path)
}

// routeMembers is /organizations/ORG/users.
func (s *Server) routeMembers(req *request, o *Org, rest []string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		switch m {
		case http.MethodGet:
			out := make([]map[string]map[string]string, 0, len(o.Members))
			for _, u := range o.Members {
				out = append(out, map[string]map[string]string{"user": {"username": u}})
			}
			return 200, out, nil
		case http.MethodPost:
			if !req.super {
				return 0, nil, fail(403, "missing create permission")
			}
			name := nameOf(req.body, "username")
			if _, ok := s.Users[name]; !ok {
				return 0, nil, fail(404, "user '%s' not found", name)
			}
			for _, u := range o.Members {
				if u == name {
					return 0, nil, fail(409, "The association already exists.")
				}
			}
			o.Members = append(o.Members, name)
			if err := s.persistOrg(o); err != nil {
				return 0, nil, err
			}
			return 201, nil, nil
		}
	}
	name := rest[0]
	member := false
	for _, u := range o.Members {
		if u == name {
			member = true
		}
	}
	if !member {
		return 0, nil, fail(404, "Cannot find a user %s in organization %s", name, o.Name)
	}
	switch m {
	case http.MethodGet:
		if u, ok := s.Users[name]; ok {
			return 200, u.Body, nil
		}
		return 200, map[string]string{"username": name}, nil
	case http.MethodDelete:
		for i, u := range o.Members {
			if u == name {
				o.Members = append(o.Members[:i], o.Members[i+1:]...)
			}
		}
		for _, g := range o.Groups {
			g.Users = without(g.Users, name)
		}
		if err := s.persistOrg(o); err != nil {
			return 0, nil, err
		}
		return 200, nil, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

func without(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// crud handles GET/POST on a collection and GET/PUT/DELETE on an object.
func (s *Server) crud(o *Org, k *kinds.Kind, store map[string]json.RawMessage, rest []string, req *request, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		switch m {
		case http.MethodGet:
			out := map[string]string{}
			for n := range store {
				out[n] = url(k.Plural, n)
			}
			return 200, out, nil
		case http.MethodPost:
			n := nameOf(req.body, "name")
			if n == "" {
				return 0, nil, fail(400, "Field 'name' missing")
			}
			if _, ok := store[n]; ok {
				return 0, nil, fail(409, "%s already exists", k.Name)
			}
			body := withName(req.body, k, n)
			store[n] = body
			if err := s.persistObject(o, k, kinds.ID{Name: n}, body); err != nil {
				return 0, nil, err
			}
			return 201, map[string]string{"uri": url(k.Plural, n)}, nil
		}
		return 0, nil, fail(405, "method not allowed")
	}
	n := rest[0]
	obj, ok := store[n]
	if !ok {
		return 0, nil, fail(404, "%s '%s' not found", k.Name, n)
	}
	switch m {
	case http.MethodGet:
		return 200, obj, nil
	case http.MethodPut:
		if k == kinds.Environment && n == "_default" {
			return 0, nil, fail(405, "The '_default' environment cannot be modified.")
		}
		body := withName(req.body, k, n)
		store[n] = body
		if err := s.persistObject(o, k, kinds.ID{Name: n}, body); err != nil {
			return 0, nil, err
		}
		return 200, body, nil
	case http.MethodDelete:
		if k == kinds.Environment && n == "_default" {
			return 0, nil, fail(405, "The '_default' environment cannot be modified.")
		}
		delete(store, n)
		delete(o.ACLs, k.Plural+"/"+n)
		if err := s.persistDelete(o, k, kinds.ID{Name: n}); err != nil {
			return 0, nil, err
		}
		return 200, obj, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// withName makes sure the stored body carries its name and the standard
// chef_type/json_class markers chef-client expects.
func withName(body json.RawMessage, k *kinds.Kind, name string) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		m = map[string]any{}
	}
	m["name"] = name
	switch k {
	case kinds.Node:
		m["chef_type"], m["json_class"] = "node", "Chef::Node"
		for _, key := range []string{"normal", "default", "override", "automatic"} {
			if _, ok := m[key]; !ok {
				m[key] = map[string]any{}
			}
		}
		if _, ok := m["run_list"]; !ok {
			m["run_list"] = []any{}
		}
		if _, ok := m["chef_environment"]; !ok {
			m["chef_environment"] = "_default"
		}
	case kinds.Role:
		m["chef_type"], m["json_class"] = "role", "Chef::Role"
		for _, key := range []string{"default_attributes", "override_attributes", "env_run_lists"} {
			if _, ok := m[key]; !ok {
				m[key] = map[string]any{}
			}
		}
		if _, ok := m["run_list"]; !ok {
			m["run_list"] = []any{}
		}
	case kinds.Environment:
		m["chef_type"], m["json_class"] = "environment", "Chef::Environment"
		for _, key := range []string{"default_attributes", "override_attributes", "cookbook_versions"} {
			if _, ok := m[key]; !ok {
				m[key] = map[string]any{}
			}
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func (s *Server) inEnvironment(o *Org, store map[string]json.RawMessage, env, col string, url func(string, string) string) map[string]string {
	out := map[string]string{}
	for n, body := range store {
		if col == "roles" || nameOf(body, "chef_environment") == env {
			out[n] = url(col, n)
		}
	}
	return out
}

// roleEnvironments is /roles/NAME/environments[/ENV].
func (s *Server) roleEnvironments(o *Org, role string, rest []string) (int, any, *apiErr) {
	var r struct {
		RunList     []string            `json:"run_list"`
		EnvRunLists map[string][]string `json:"env_run_lists"`
	}
	_ = json.Unmarshal(o.Roles[role], &r)
	if len(rest) == 0 {
		out := []string{"_default"}
		for e := range r.EnvRunLists {
			out = append(out, e)
		}
		sort.Strings(out)
		return 200, out, nil
	}
	if l, ok := r.EnvRunLists[rest[0]]; ok {
		return 200, map[string]any{"run_list": nonNil(l)}, nil
	}
	return 200, map[string]any{"run_list": nonNil(r.RunList)}, nil
}

// ---------------------------------------------------------------------------
// Clients
// ---------------------------------------------------------------------------

func (s *Server) routeClients(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		switch m {
		case http.MethodGet:
			out := map[string]string{}
			for n := range o.Clients {
				out[n] = url("clients", n)
			}
			return 200, out, nil
		case http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(req.body, &in)
			name, _ := in["name"].(string)
			if name == "" {
				name, _ = in["clientname"].(string)
			}
			if name == "" {
				return 0, nil, fail(400, "Field 'name' missing")
			}
			if _, ok := o.Clients[name]; ok {
				return 0, nil, fail(409, "Client already exists")
			}
			validator, _ := in["validator"].(bool)
			pub, _ := in["public_key"].(string)
			createKey, hasCreate := in["create_key"].(bool)
			if !hasCreate {
				createKey = pub == "" // v0 clients expect a key back
			}
			var priv []byte
			if pub == "" && createKey {
				var err error
				priv, pub, err = generateKeyPair()
				if err != nil {
					return 0, nil, fail(500, "generating key: %v", err)
				}
			}
			o.Clients[name], _ = json.Marshal(map[string]any{"name": name, "clientname": name, "validator": validator, "orgname": o.Name, "json_class": "Chef::ApiClient", "chef_type": "client"})
			o.ClientKeys[name] = map[string]string{}
			if pub != "" {
				o.ClientKeys[name]["default"] = pub
			}
			o.Groups["clients"].Clients = appendUnique(o.Groups["clients"].Clients, name)
			if err := s.persistClient(o, name); err != nil {
				return 0, nil, err
			}
			if err := s.persistGroup(o, "clients"); err != nil {
				return 0, nil, err
			}
			out := map[string]any{"uri": url("clients", name), "name": name, "clientname": name, "validator": validator}
			if priv != nil {
				out["private_key"] = string(priv)
				out["public_key"] = pub
				out["chef_key"] = map[string]any{"name": "default", "public_key": pub, "private_key": string(priv), "expiration_date": "infinity", "uri": url("clients", name) + "/keys/default"}
			}
			return 201, out, nil
		}
		return 0, nil, fail(405, "method not allowed")
	}
	name := rest[0]
	body, ok := o.Clients[name]
	if !ok {
		return 0, nil, fail(404, "client '%s' not found", name)
	}
	if len(rest) >= 2 {
		switch rest[1] {
		case "keys":
			if o.ClientKeys[name] == nil {
				o.ClientKeys[name] = map[string]string{}
			}
			return s.routeKeys(req, o.ClientKeys[name], func(k map[string]string) *apiErr {
				o.ClientKeys[name] = k
				return s.persistClient(o, name)
			}, rest[2:])
		case "_acl":
			key := "clients/" + name
			return s.routeACL(req, o.aclOf(key, []string{name}), rest[2:], func() *apiErr { return s.persistACL(o, key) })
		}
	}
	switch m {
	case http.MethodGet:
		if req.version == 0 {
			var mm map[string]any
			_ = json.Unmarshal(body, &mm)
			mm["public_key"] = o.ClientKeys[name]["default"]
			return 200, mm, nil
		}
		return 200, body, nil
	case http.MethodPut:
		var in map[string]any
		_ = json.Unmarshal(req.body, &in)
		var cur map[string]any
		_ = json.Unmarshal(body, &cur)
		if v, ok := in["validator"].(bool); ok {
			cur["validator"] = v
		}
		if pub, _ := in["public_key"].(string); pub != "" && req.version == 0 {
			o.ClientKeys[name]["default"] = pub
		}
		if pk, _ := in["private_key"].(bool); pk && req.version == 0 {
			priv, pub, err := generateKeyPair()
			if err != nil {
				return 0, nil, fail(500, "generating key: %v", err)
			}
			o.ClientKeys[name]["default"] = pub
			cur["private_key"] = string(priv)
		}
		o.Clients[name], _ = json.Marshal(map[string]any{"name": name, "clientname": name, "validator": cur["validator"], "orgname": o.Name, "json_class": "Chef::ApiClient", "chef_type": "client"})
		if err := s.persistClient(o, name); err != nil {
			return 0, nil, err
		}
		cur["public_key"] = o.ClientKeys[name]["default"]
		return 200, cur, nil
	case http.MethodDelete:
		delete(o.Clients, name)
		delete(o.ClientKeys, name)
		delete(o.ACLs, "clients/"+name)
		for _, g := range o.Groups {
			g.Clients = without(g.Clients, name)
		}
		if err := s.persistDelete(o, kinds.Client, kinds.ID{Name: name}); err != nil {
			return 0, nil, err
		}
		if err := s.persistGroup(o, "clients"); err != nil {
			return 0, nil, err
		}
		return 200, body, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func (s *Server) routeGroups(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		switch m {
		case http.MethodGet:
			out := map[string]string{}
			for n := range o.Groups {
				out[n] = url("groups", n)
			}
			return 200, out, nil
		case http.MethodPost:
			name := nameOf(req.body, "groupname", "name")
			if name == "" {
				return 0, nil, fail(400, "Field 'groupname' missing")
			}
			if _, ok := o.Groups[name]; ok {
				return 0, nil, fail(409, "Group already exists")
			}
			o.Groups[name] = &Group{Users: []string{}, Clients: []string{}, Groups: []string{}}
			if err := s.persistGroup(o, name); err != nil {
				return 0, nil, err
			}
			return 201, map[string]string{"uri": url("groups", name)}, nil
		}
	}
	name := rest[0]
	g, ok := o.Groups[name]
	if !ok {
		return 0, nil, fail(404, "group '%s' not found", name)
	}
	if len(rest) >= 2 && rest[1] == "_acl" {
		key := "groups/" + name
		return s.routeACL(req, o.aclOf(key, nil), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}
	switch m {
	case http.MethodGet:
		actors := append(append([]string{}, g.Users...), g.Clients...)
		return 200, map[string]any{"actors": actors, "users": nonNil(g.Users), "clients": nonNil(g.Clients), "groups": nonNil(g.Groups), "orgname": o.Name, "name": name, "groupname": name}, nil
	case http.MethodPut:
		var in struct {
			Actors *Group `json:"actors"`
		}
		if err := json.Unmarshal(req.body, &in); err != nil || in.Actors == nil {
			return 0, nil, fail(400, "Field 'actors' missing")
		}
		for _, u := range in.Actors.Users {
			if _, ok := s.Users[u]; !ok && u != "pivotal" {
				return 0, nil, fail(400, "user '%s' does not exist", u)
			}
		}
		for _, c := range in.Actors.Clients {
			if _, ok := o.Clients[c]; !ok {
				return 0, nil, fail(400, "client '%s' does not exist", c)
			}
		}
		for _, sg := range in.Actors.Groups {
			if _, ok := o.Groups[sg]; !ok {
				return 0, nil, fail(400, "group '%s' does not exist", sg)
			}
		}
		g.Users, g.Clients, g.Groups = nonNil(in.Actors.Users), nonNil(in.Actors.Clients), nonNil(in.Actors.Groups)
		if err := s.persistGroup(o, name); err != nil {
			return 0, nil, err
		}
		return 200, nil, nil
	case http.MethodDelete:
		delete(o.Groups, name)
		delete(o.ACLs, "groups/"+name)
		if err := s.persistDelete(o, kinds.Group, kinds.ID{Name: name}); err != nil {
			return 0, nil, err
		}
		return 200, nil, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// Data bags
// ---------------------------------------------------------------------------

func (s *Server) routeData(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		switch m {
		case http.MethodGet:
			out := map[string]string{}
			for n := range o.DataBags {
				out[n] = url("data", n)
			}
			return 200, out, nil
		case http.MethodPost:
			name := nameOf(req.body, "name")
			if name == "" {
				return 0, nil, fail(400, "Field 'name' missing")
			}
			if _, ok := o.DataBags[name]; ok {
				return 0, nil, fail(409, "Data bag already exists")
			}
			o.DataBags[name] = map[string]json.RawMessage{}
			if err := s.persistObject(o, kinds.DataBag, kinds.ID{Name: name}, nil); err != nil {
				return 0, nil, err
			}
			return 201, map[string]string{"uri": url("data", name)}, nil
		}
	}
	bagName := rest[0]
	bag, ok := o.DataBags[bagName]
	if !ok {
		return 0, nil, fail(404, "Cannot load data bag %s", bagName)
	}
	if len(rest) >= 2 && rest[1] == "_acl" {
		key := "data_bags/" + bagName
		return s.routeACL(req, o.aclOf(key, nil), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}
	if len(rest) == 1 {
		switch m {
		case http.MethodGet:
			out := map[string]string{}
			for n := range bag {
				out[n] = url("data/"+bagName, n)
			}
			return 200, out, nil
		case http.MethodPost:
			id := nameOf(req.body, "id")
			if id == "" {
				return 0, nil, fail(400, "Field 'id' missing")
			}
			if _, ok := bag[id]; ok {
				return 0, nil, fail(409, "Data Bag Item already exists")
			}
			bag[id] = req.body
			if err := s.persistObject(o, kinds.DataBagItem, kinds.ID{Name: bagName, Sub: id}, req.body); err != nil {
				return 0, nil, err
			}
			return 201, req.body, nil
		case http.MethodDelete:
			delete(o.DataBags, bagName)
			delete(o.ACLs, "data_bags/"+bagName)
			if err := s.persistDelete(o, kinds.DataBag, kinds.ID{Name: bagName}); err != nil {
				return 0, nil, err
			}
			return 200, map[string]any{"name": bagName, "json_class": "Chef::DataBag", "chef_type": "data_bag"}, nil
		}
	}
	itemName := rest[1]
	item, ok := bag[itemName]
	if !ok {
		return 0, nil, fail(404, "Cannot load data bag item %s for data bag %s", itemName, bagName)
	}
	switch m {
	case http.MethodGet:
		return 200, item, nil
	case http.MethodPut:
		bag[itemName] = req.body
		if err := s.persistObject(o, kinds.DataBagItem, kinds.ID{Name: bagName, Sub: itemName}, req.body); err != nil {
			return 0, nil, err
		}
		return 200, req.body, nil
	case http.MethodDelete:
		delete(bag, itemName)
		if err := s.persistDelete(o, kinds.DataBagItem, kinds.ID{Name: bagName, Sub: itemName}); err != nil {
			return 0, nil, err
		}
		return 200, item, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// Cookbooks and artifacts
// ---------------------------------------------------------------------------

// manifestFor renders a stored manifest for a client: bookshelf URLs added,
// and the segment layout for API versions below 2.
func (s *Server) manifestFor(raw json.RawMessage, version int) json.RawMessage {
	var mf map[string]any
	_ = json.Unmarshal(raw, &mf)
	files, _ := mf["all_files"].([]any)
	for _, f := range files {
		if fm, ok := f.(map[string]any); ok {
			fm["url"] = s.base + "/bookshelf/" + fm["checksum"].(string)
		}
	}
	if version < 2 {
		segments := map[string][]any{}
		for _, seg := range []string{"attributes", "definitions", "files", "libraries", "providers", "recipes", "resources", "templates", "root_files"} {
			segments[seg] = []any{}
		}
		for _, f := range files {
			fm, _ := f.(map[string]any)
			name, _ := fm["name"].(string)
			seg, base, _ := strings.Cut(name, "/")
			if _, ok := segments[seg]; !ok {
				seg = "root_files"
			}
			entry := map[string]any{"name": base, "path": fm["path"], "checksum": fm["checksum"], "specificity": fm["specificity"], "url": fm["url"]}
			segments[seg] = append(segments[seg], entry)
		}
		delete(mf, "all_files")
		for seg, list := range segments {
			mf[seg] = list
		}
	}
	out, _ := json.Marshal(mf)
	return out
}

func (s *Server) routeCookbooks(req *request, o *Org, col string, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	k := kinds.Cookbook
	store := o.Cookbooks
	if col == "cookbook_artifacts" {
		k, store = kinds.Artifact, o.Artifacts
	}
	numVersions := func() int {
		nv := req.r.URL.Query().Get("num_versions")
		if nv == "" {
			return 1
		}
		if nv == "all" {
			return -1
		}
		n, _ := strconv.Atoi(nv)
		return n
	}
	list := func(names []string, limit int) map[string]any {
		out := map[string]any{}
		for _, n := range names {
			versions := []map[string]string{}
			subs := sortedKeys(store[n])
			if k == kinds.Cookbook {
				deps.SortVersions(subs)
				for i, j := 0, len(subs)-1; i < j; i, j = i+1, j-1 {
					subs[i], subs[j] = subs[j], subs[i] // newest first
				}
			}
			for i, v := range subs {
				if limit >= 0 && i >= limit {
					break
				}
				e := map[string]string{"url": url(col, n+"/"+v)}
				if k == kinds.Cookbook {
					e["version"] = v
				} else {
					e["identifier"] = v
				}
				versions = append(versions, e)
			}
			out[n] = map[string]any{"url": url(col, n), "versions": versions}
		}
		return out
	}
	if len(rest) == 0 {
		return 200, list(sortedKeys(store), numVersions()), nil
	}
	name := rest[0]
	if k == kinds.Cookbook && len(rest) == 1 {
		switch name {
		case "_latest":
			out := map[string]string{}
			for n := range store {
				if latest, ok := deps.Latest(sortedKeys(store[n])); ok {
					out[n] = url(col, n+"/"+latest)
				}
			}
			return 200, out, nil
		case "_recipes":
			return 200, s.recipes(o, "_default"), nil
		}
	}
	if len(rest) >= 2 && rest[1] == "_acl" {
		if _, ok := store[name]; !ok {
			return 0, nil, fail(404, "%s '%s' not found", k.Name, name)
		}
		key := col + "/" + name
		return s.routeACL(req, o.aclOf(key, nil), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}
	if len(rest) == 1 {
		if _, ok := store[name]; !ok {
			return 0, nil, fail(404, "%s '%s' not found", k.Name, name)
		}
		return 200, list([]string{name}, numVersions()), nil
	}
	sub := rest[1]
	if k == kinds.Cookbook && sub == "_latest" {
		if latest, ok := deps.Latest(sortedKeys(store[name])); ok {
			sub = latest
		}
	}
	switch m {
	case http.MethodGet:
		raw, ok := store[name][sub]
		if !ok {
			return 0, nil, fail(404, "%s '%s' version '%s' not found", k.Name, name, sub)
		}
		return 200, s.manifestFor(raw, req.version), nil
	case http.MethodPut:
		var mf map[string]any
		if err := json.Unmarshal(req.body, &mf); err != nil {
			return 0, nil, fail(400, "invalid manifest")
		}
		// Accept segment-form uploads (API < 2) by folding them into all_files.
		if _, ok := mf["all_files"]; !ok {
			var all []any
			for _, seg := range []string{"attributes", "definitions", "files", "libraries", "providers", "recipes", "resources", "templates", "root_files"} {
				entries, _ := mf[seg].([]any)
				for _, e := range entries {
					em, _ := e.(map[string]any)
					base, _ := em["name"].(string)
					em["name"] = seg + "/" + base
					all = append(all, em)
				}
				delete(mf, seg)
			}
			if all == nil {
				all = []any{}
			}
			mf["all_files"] = all
		}
		files, _ := mf["all_files"].([]any)
		for _, f := range files {
			fm, _ := f.(map[string]any)
			sum, _ := fm["checksum"].(string)
			if _, ok := s.Bookshelf[sum]; !ok {
				return 0, nil, fail(400, "Manifest has a checksum that hasn't been uploaded: %s", sum)
			}
			delete(fm, "url")
		}
		wantName := name + "-" + sub
		if k == kinds.Artifact {
			wantName = name
		}
		if got, _ := mf["name"].(string); got != wantName {
			return 0, nil, fail(400, "Field 'name' invalid : %s does not match %s", got, wantName)
		}
		frozenKey := col + "/" + name + "/" + sub
		if _, exists := store[name][sub]; exists && k == kinds.Artifact {
			return 0, nil, fail(409, "Cookbook artifact already exists")
		}
		if o.Frozen[frozenKey] && req.r.URL.Query().Get("force") != "true" {
			return 0, nil, fail(409, "The cookbook %s at version %s is frozen. Use the 'force' option to override.", name, sub)
		}
		if store[name] == nil {
			store[name] = map[string]json.RawMessage{}
		}
		created := true
		if _, ok := store[name][sub]; ok {
			created = false
		}
		if frozen, _ := mf["frozen?"].(bool); frozen {
			o.Frozen[frozenKey] = true
		}
		mf["frozen?"] = o.Frozen[frozenKey]
		if k == kinds.Cookbook {
			mf["cookbook_name"], mf["version"], mf["chef_type"], mf["json_class"] = name, sub, "cookbook_version", "Chef::CookbookVersion"
		} else {
			mf["identifier"], mf["chef_type"] = sub, "cookbook_version"
		}
		raw, _ := json.Marshal(mf)
		store[name][sub] = raw
		if err := s.persistCookbook(o, k, kinds.ID{Name: name, Sub: sub}, raw); err != nil {
			return 0, nil, err
		}
		if created {
			return 201, s.manifestFor(raw, req.version), nil
		}
		return 200, s.manifestFor(raw, req.version), nil
	case http.MethodDelete:
		raw, ok := store[name][sub]
		if !ok {
			return 0, nil, fail(404, "%s '%s' version '%s' not found", k.Name, name, sub)
		}
		delete(store[name], sub)
		delete(o.Frozen, col+"/"+name+"/"+sub)
		if len(store[name]) == 0 {
			delete(store, name)
			delete(o.ACLs, col+"/"+name)
		}
		if err := s.persistDelete(o, k, kinds.ID{Name: name, Sub: sub}); err != nil {
			return 0, nil, err
		}
		return 200, s.manifestFor(raw, req.version), nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// envCookbooks is /environments/ENV/cookbooks[/NAME]: versions allowed by
// the environment's constraints.
func (s *Server) envCookbooks(o *Org, env string, rest []string, req *request) (int, any, *apiErr) {
	var e struct {
		CookbookVersions map[string]string `json:"cookbook_versions"`
	}
	_ = json.Unmarshal(o.Environments[env], &e)
	names := sortedKeys(o.Cookbooks)
	if len(rest) == 1 {
		if _, ok := o.Cookbooks[rest[0]]; !ok {
			return 0, nil, fail(404, "cookbook '%s' not found", rest[0])
		}
		names = []string{rest[0]}
	}
	limit := 1
	if nv := req.r.URL.Query().Get("num_versions"); nv == "all" {
		limit = -1
	} else if nv != "" {
		limit, _ = strconv.Atoi(nv)
	}
	out := map[string]any{}
	for _, n := range names {
		var allowed []string
		for v := range o.Cookbooks[n] {
			if c, ok := e.CookbookVersions[n]; ok {
				con, err := deps.ParseConstraint(c)
				pv, err2 := deps.ParseVersion(v)
				if err != nil || err2 != nil || !con.Match(pv) {
					continue
				}
			}
			allowed = append(allowed, v)
		}
		deps.SortVersions(allowed)
		versions := []map[string]string{}
		for i := len(allowed) - 1; i >= 0; i-- {
			if limit >= 0 && len(versions) >= limit {
				break
			}
			versions = append(versions, map[string]string{"version": allowed[i], "url": s.orgURL(o.Name) + "/cookbooks/" + n + "/" + allowed[i]})
		}
		out[n] = map[string]any{"url": s.orgURL(o.Name) + "/cookbooks/" + n, "versions": versions}
	}
	return 200, out, nil
}

// recipes lists cookbook::recipe names of the latest versions.
func (s *Server) recipes(o *Org, env string) []string {
	var out []string
	for n, versions := range o.Cookbooks {
		latest, ok := deps.Latest(sortedKeys(versions))
		if !ok {
			continue
		}
		var mf struct {
			Files []struct {
				Name string `json:"name"`
			} `json:"all_files"`
		}
		_ = json.Unmarshal(versions[latest], &mf)
		for _, f := range mf.Files {
			if strings.HasPrefix(f.Name, "recipes/") && strings.HasSuffix(f.Name, ".rb") {
				r := strings.TrimSuffix(strings.TrimPrefix(f.Name, "recipes/"), ".rb")
				if r == "default" {
					out = append(out, n)
				} else {
					out = append(out, n+"::"+r)
				}
			}
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// universe is the Berkshelf/Policyfile view of every cookbook version.
func (s *Server) universe(o *Org) map[string]any {
	out := map[string]any{}
	for n, versions := range o.Cookbooks {
		vm := map[string]any{}
		for v, raw := range versions {
			var mf struct {
				Metadata struct {
					Dependencies map[string]string `json:"dependencies"`
				} `json:"metadata"`
			}
			_ = json.Unmarshal(raw, &mf)
			depsMap := mf.Metadata.Dependencies
			if depsMap == nil {
				depsMap = map[string]string{}
			}
			vm[v] = map[string]any{"location_type": "chef_server", "location_path": s.orgURL(o.Name), "download_url": s.orgURL(o.Name) + "/cookbooks/" + n + "/" + v + "/download", "dependencies": depsMap}
		}
		out[n] = vm
	}
	return out
}

func (s *Server) routeSandboxes(req *request, rest []string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 && m == http.MethodPost {
		var in struct {
			Checksums map[string]any `json:"checksums"`
		}
		if err := json.Unmarshal(req.body, &in); err != nil {
			return 0, nil, fail(400, "invalid body")
		}
		id := strconv.FormatInt(time.Now().UnixNano(), 36)
		out := map[string]any{}
		need := map[string]bool{}
		for sum := range in.Checksums {
			_, have := s.Bookshelf[sum]
			out[sum] = map[string]any{"needs_upload": !have, "url": s.base + "/bookshelf/" + sum}
			if !have {
				need[sum] = true
			}
		}
		s.sandboxes[id] = need
		return 201, map[string]any{"uri": s.base + req.r.URL.Path + "/" + id, "sandbox_id": id, "checksums": out}, nil
	}
	if len(rest) == 1 && m == http.MethodPut {
		need, ok := s.sandboxes[rest[0]]
		if !ok {
			return 0, nil, fail(404, "sandbox '%s' not found", rest[0])
		}
		for sum := range need {
			if _, ok := s.Bookshelf[sum]; !ok {
				return 0, nil, fail(400, "Cannot update sandbox %s: checksum %s was not uploaded", rest[0], sum)
			}
		}
		delete(s.sandboxes, rest[0])
		return 200, map[string]any{"guid": rest[0], "name": rest[0], "checksums": sortedKeys(need), "create_time": time.Now().UTC().Format(time.RFC3339), "is_completed": true}, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

func (s *Server) routePolicies(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	if len(rest) == 0 {
		out := map[string]any{}
		for n, revs := range o.Policies {
			rm := map[string]any{}
			for r := range revs {
				rm[r] = map[string]any{}
			}
			out[n] = map[string]any{"uri": url("policies", n), "revisions": rm}
		}
		return 200, out, nil
	}
	name := rest[0]
	if len(rest) >= 2 && rest[1] == "_acl" {
		if _, ok := o.Policies[name]; !ok {
			return 0, nil, fail(404, "policy '%s' not found", name)
		}
		key := "policies/" + name
		return s.routeACL(req, o.aclOf(key, nil), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}
	if len(rest) == 1 {
		revs, ok := o.Policies[name]
		if !ok {
			return 0, nil, fail(404, "policy '%s' not found", name)
		}
		if m == http.MethodDelete {
			for r := range revs {
				if err := s.persistDelete(o, kinds.Policy, kinds.ID{Name: name, Sub: r}); err != nil {
					return 0, nil, err
				}
			}
			delete(o.Policies, name)
			delete(o.ACLs, "policies/"+name)
			return 200, nil, nil
		}
		rm := map[string]any{}
		for r := range revs {
			rm[r] = map[string]any{}
		}
		return 200, map[string]any{"revisions": rm}, nil
	}
	if rest[1] != "revisions" {
		return 0, nil, fail(404, "no route")
	}
	if len(rest) == 2 {
		switch m {
		case http.MethodGet:
			rm := map[string]any{}
			for r := range o.Policies[name] {
				rm[r] = map[string]any{}
			}
			return 200, rm, nil
		case http.MethodPost:
			rev := nameOf(req.body, "revision_id")
			if rev == "" {
				return 0, nil, fail(400, "Field 'revision_id' missing")
			}
			if o.Policies[name] == nil {
				o.Policies[name] = map[string]json.RawMessage{}
			}
			if _, ok := o.Policies[name][rev]; ok {
				return 0, nil, fail(409, "Policy revision already exists")
			}
			o.Policies[name][rev] = req.body
			if err := s.persistObject(o, kinds.Policy, kinds.ID{Name: name, Sub: rev}, req.body); err != nil {
				return 0, nil, err
			}
			return 201, req.body, nil
		}
	}
	rev := rest[2]
	doc, ok := o.Policies[name][rev]
	if !ok {
		return 0, nil, fail(404, "policy revision '%s' of '%s' not found", rev, name)
	}
	switch m {
	case http.MethodGet:
		return 200, doc, nil
	case http.MethodDelete:
		delete(o.Policies[name], rev)
		if len(o.Policies[name]) == 0 {
			delete(o.Policies, name)
		}
		if err := s.persistDelete(o, kinds.Policy, kinds.ID{Name: name, Sub: rev}); err != nil {
			return 0, nil, err
		}
		return 200, doc, nil
	}
	return 0, nil, fail(405, "method not allowed")
}

func (s *Server) routePolicyGroups(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	m := req.r.Method
	render := func(g string) map[string]any {
		pm := map[string]any{}
		for p, r := range o.PolicyGroups[g] {
			pm[p] = map[string]string{"revision_id": r}
		}
		return map[string]any{"uri": url("policy_groups", g), "policies": pm}
	}
	if len(rest) == 0 {
		out := map[string]any{}
		for g := range o.PolicyGroups {
			out[g] = render(g)
		}
		return 200, out, nil
	}
	g := rest[0]
	if len(rest) >= 2 && rest[1] == "_acl" {
		if _, ok := o.PolicyGroups[g]; !ok {
			return 0, nil, fail(404, "policy group '%s' not found", g)
		}
		key := "policy_groups/" + g
		return s.routeACL(req, o.aclOf(key, nil), rest[2:], func() *apiErr { return s.persistACL(o, key) })
	}
	if len(rest) == 1 {
		if _, ok := o.PolicyGroups[g]; !ok {
			return 0, nil, fail(404, "policy group '%s' not found", g)
		}
		switch m {
		case http.MethodGet:
			return 200, render(g), nil
		case http.MethodDelete:
			out := render(g)
			delete(o.PolicyGroups, g)
			delete(o.ACLs, "policy_groups/"+g)
			if err := s.persistDelete(o, kinds.PolicyGroup, kinds.ID{Name: g}); err != nil {
				return 0, nil, err
			}
			return 200, out, nil
		}
	}
	if rest[1] != "policies" || len(rest) < 3 {
		return 0, nil, fail(404, "no route")
	}
	policy := rest[2]
	switch m {
	case http.MethodPut:
		rev := nameOf(req.body, "revision_id")
		if rev == "" {
			return 0, nil, fail(400, "Field 'revision_id' missing")
		}
		if o.Policies[policy] == nil {
			o.Policies[policy] = map[string]json.RawMessage{}
		}
		if _, ok := o.Policies[policy][rev]; !ok {
			o.Policies[policy][rev] = req.body
			if err := s.persistObject(o, kinds.Policy, kinds.ID{Name: policy, Sub: rev}, req.body); err != nil {
				return 0, nil, err
			}
		}
		if o.PolicyGroups[g] == nil {
			o.PolicyGroups[g] = map[string]string{}
		}
		o.PolicyGroups[g][policy] = rev
		if err := s.persistPolicyGroup(o, g); err != nil {
			return 0, nil, err
		}
		return 200, req.body, nil
	case http.MethodGet:
		rev, ok := o.PolicyGroups[g][policy]
		if !ok {
			return 0, nil, fail(404, "policy '%s' is not assigned to group '%s'", policy, g)
		}
		return 200, o.Policies[policy][rev], nil
	case http.MethodDelete:
		rev, ok := o.PolicyGroups[g][policy]
		if !ok {
			return 0, nil, fail(404, "policy '%s' is not assigned to group '%s'", policy, g)
		}
		delete(o.PolicyGroups[g], policy)
		if err := s.persistPolicyGroup(o, g); err != nil {
			return 0, nil, err
		}
		return 200, o.Policies[policy][rev], nil
	}
	return 0, nil, fail(405, "method not allowed")
}

// ---------------------------------------------------------------------------
// Depsolver
// ---------------------------------------------------------------------------

// depsolve resolves run list recipes to cookbook versions, honouring the
// environment's constraints and recipe[name@version] pins, greedily
// choosing the newest version that satisfies each metadata dependency.
func (s *Server) depsolve(o *Org, env string, body []byte, version int) (int, any, *apiErr) {
	var in struct {
		RunList []string `json:"run_list"`
	}
	_ = json.Unmarshal(body, &in)
	envBody, ok := o.Environments[env]
	if !ok {
		return 0, nil, fail(404, "environment '%s' not found", env)
	}
	var envDoc struct {
		CookbookVersions map[string]string `json:"cookbook_versions"`
	}
	_ = json.Unmarshal(envBody, &envDoc)
	chosen := map[string]string{}
	var missing, noVersion []string
	var walk func(name, constraint string) bool
	walk = func(name, constraint string) bool {
		if _, done := chosen[name]; done {
			return true
		}
		if _, ok := o.Cookbooks[name]; !ok {
			return false
		}
		var versions []string
		for v := range o.Cookbooks[name] {
			if pin, ok := envDoc.CookbookVersions[name]; ok {
				if c, err := deps.ParseConstraint(pin); err == nil {
					if pv, err := deps.ParseVersion(v); err == nil && !c.Match(pv) {
						continue
					}
				}
			}
			versions = append(versions, v)
		}
		best, ok := deps.Best(versions, constraint)
		if !ok {
			noVersion = append(noVersion, name)
			return false
		}
		chosen[name] = best
		var mf struct {
			Metadata struct {
				Dependencies map[string]string `json:"dependencies"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(o.Cookbooks[name][best], &mf)
		for dep, c := range mf.Metadata.Dependencies {
			if !walk(dep, c) {
				return false
			}
		}
		return true
	}
	for _, r := range in.RunList {
		item := deps.ParseRunListItem(r)
		name := item.Cookbook()
		constraint := ">= 0.0.0"
		if i := strings.IndexByte(r, '@'); i >= 0 {
			constraint = "= " + strings.TrimSuffix(r[i+1:], "]")
		}
		if _, ok := o.Cookbooks[name]; !ok {
			missing = append(missing, r)
			continue
		}
		if !walk(name, constraint) {
			if len(noVersion) == 0 {
				missing = append(missing, r)
			}
		}
	}
	if len(missing) > 0 || len(noVersion) > 0 {
		msg := "Run list contains invalid items: no such cookbooks " + strings.Join(missing, ", ") + "."
		if len(missing) == 0 {
			msg = "Unable to satisfy constraints on cookbooks " + strings.Join(noVersion, ", ") + "."
		}
		return 412, map[string]any{"error": []any{map[string]any{"message": msg, "non_existent_cookbooks": nonNil(missing), "cookbooks_with_no_versions": nonNil(noVersion)}}}, nil
	}
	out := map[string]any{}
	for name, v := range chosen {
		out[name] = s.manifestFor(o.Cookbooks[name][v], version)
	}
	return 200, out, nil
}
