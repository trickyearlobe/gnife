package kinds

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/trickyearlobe/gnife/internal/chef"
)

// Restore order. Dependencies land before dependants; ACLs (not a kind) go last.
const (
	OrderOrganization = 1
	OrderContainer    = 2
	OrderGroup        = 3
	OrderClient       = 4
	OrderUser         = 5
	OrderEnvironment  = 6
	OrderCookbook     = 7
	OrderArtifact     = 8
	OrderRole         = 9
	OrderDataBag      = 10
	OrderDataBagItem  = 11
	OrderNode         = 12
	OrderPolicy       = 13
	OrderPolicyGroup  = 14
)

var (
	Organization = register(organizationKind())
	Container    = register(containerKind())
	Group        = register(groupKind())
	Client       = register(clientKind())
	User         = register(userKind())
	Environment  = register(environmentKind())
	Cookbook     = register(filesKind("cookbook", "cookbooks", "version", OrderCookbook))
	Artifact     = register(filesKind("artifact", "cookbook_artifacts", "identifier", OrderArtifact))
	Role         = register(simpleJSON("role", "roles", OrderRole, false))
	DataBag      = register(dataBagKind())
	DataBagItem  = register(dataBagItemKind())
	Node         = register(simpleJSON("node", "nodes", OrderNode, false))
	Policy       = register(policyKind())
	PolicyGroup  = register(policyGroupKind())
)

func environmentKind() *Kind {
	k := simpleJSON("environment", "environments", OrderEnvironment, false)
	put := k.Put
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if id.Name == "_default" {
			return nil // immutable on every server
		}
		return put(ctx, c, id, body, exists)
	}
	k.Delete = func(ctx context.Context, c *chef.Client, id ID) error {
		if id.Name == "_default" {
			return fmt.Errorf("the _default environment cannot be deleted")
		}
		return c.Delete(ctx, k.ObjectPath(c, id), nil)
	}
	return k
}

func containerKind() *Kind {
	k := simpleJSON("container", "containers", OrderContainer, false)
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if exists {
			return nil // containers carry no mutable data
		}
		return c.Post(ctx, k.BasePath(c), map[string]string{"containername": id.Name}, nil)
	}
	return k
}

// groupActors is the write form of group membership.
type groupActors struct {
	Users   []string `json:"users"`
	Clients []string `json:"clients"`
	Groups  []string `json:"groups"`
}

// GroupMembers extracts membership from a group body in either the read
// form (top-level users/clients/groups) or the write form (actors object).
func GroupMembers(body json.RawMessage) (users, clients, groups []string) {
	var g struct {
		Users   []string        `json:"users"`
		Clients []string        `json:"clients"`
		Groups  []string        `json:"groups"`
		Actors  json.RawMessage `json:"actors"`
	}
	_ = json.Unmarshal(body, &g)
	users, clients, groups = g.Users, g.Clients, g.Groups
	var a groupActors
	if len(g.Actors) > 0 && json.Unmarshal(g.Actors, &a) == nil {
		if users == nil {
			users = a.Users
		}
		if clients == nil {
			clients = a.Clients
		}
		if groups == nil {
			groups = a.Groups
		}
	}
	return nonNil(users), nonNil(clients), nonNil(groups)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// GroupWriteBody builds the PUT body for a group. With members=false the
// group is written empty (restore's first pass, before members exist).
func GroupWriteBody(name string, body json.RawMessage, members bool) map[string]any {
	actors := groupActors{Users: []string{}, Clients: []string{}, Groups: []string{}}
	if members {
		actors.Users, actors.Clients, actors.Groups = GroupMembers(body)
	}
	return map[string]any{"groupname": name, "actors": actors}
}

// IsUSAG reports whether a group name is a user-specific access group: a
// 32-hex-digit group the server creates for each organisation member and
// recreates itself on association. They are never listed, copied or purged.
func IsUSAG(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, c := range name {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func groupKind() *Kind {
	k := simpleJSON("group", "groups", OrderGroup, false)
	k.List = func(ctx context.Context, c *chef.Client) ([]ID, error) {
		ids, err := listMap(ctx, c, k.BasePath(c))
		if err != nil {
			return nil, err
		}
		out := ids[:0]
		for _, id := range ids {
			if !IsUSAG(id.Name) {
				out = append(out, id)
			}
		}
		return out, nil
	}
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if !exists {
			if err := c.Post(ctx, k.BasePath(c), map[string]string{"groupname": id.Name}, nil); err != nil && !chef.IsConflict(err) {
				return err
			}
		}
		return c.Put(ctx, k.ObjectPath(c, id), GroupWriteBody(id.Name, body, true), nil)
	}
	return k
}

// PutGroupShell creates a group without members (or leaves an existing one alone).
func PutGroupShell(ctx context.Context, c *chef.Client, name string) error {
	err := c.Post(ctx, Group.BasePath(c), map[string]string{"groupname": name}, nil)
	if err != nil && !chef.IsConflict(err) {
		return err
	}
	return nil
}

// actorKind builds the client and user kinds, which carry their default
// public key alongside the object so a copy or backup is self-contained.
func actorKind(name, plural, nameField string, order int, serverLevel bool) *Kind {
	k := simpleJSON(name, plural, order, serverLevel)
	get := k.Get
	k.Get = func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error) {
		body, err := get(ctx, c, id)
		if err != nil {
			return nil, err
		}
		if Field(body, "public_key") != "" {
			return body, nil
		}
		var key struct {
			PublicKey string `json:"public_key"`
		}
		if err := c.Get(ctx, k.ObjectPath(c, id)+"/keys/default", &key); err == nil && key.PublicKey != "" {
			return SetField(body, "public_key", key.PublicKey)
		}
		return body, nil
	}
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return err
		}
		m[nameField] = id.Name
		pub, _ := m["public_key"].(string)
		delete(m, "public_key")
		delete(m, "private_key")
		delete(m, "orgname")
		delete(m, "json_class")
		delete(m, "chef_type")
		if !exists {
			m["create_key"] = false
			if pub != "" {
				m["public_key"] = pub
			}
			if name == "user" {
				if _, ok := m["password"]; !ok {
					m["password"] = randomPassword()
				}
				if _, ok := m["display_name"]; !ok {
					m["display_name"] = id.Name
				}
			}
			return c.Post(ctx, k.BasePath(c), m, nil)
		}
		delete(m, "password")
		if err := c.Put(ctx, k.ObjectPath(c, id), m, nil); err != nil {
			return err
		}
		if pub == "" {
			return nil
		}
		keyBody := map[string]any{"name": "default", "public_key": pub, "expiration_date": "infinity"}
		err := c.Put(ctx, k.ObjectPath(c, id)+"/keys/default", keyBody, nil)
		if chef.IsNotFound(err) {
			err = c.Post(ctx, k.ObjectPath(c, id)+"/keys", keyBody, nil)
		}
		return err
	}
	return k
}

func clientKind() *Kind { return actorKind("client", "clients", "name", OrderClient, false) }
func userKind() *Kind   { return actorKind("user", "users", "username", OrderUser, true) }

func randomPassword() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func organizationKind() *Kind {
	k := simpleJSON("org", "organizations", OrderOrganization, true)
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		full := Field(body, "full_name")
		if full == "" {
			full = id.Name
		}
		m := map[string]string{"name": id.Name, "full_name": full}
		if exists {
			return c.Put(ctx, k.ObjectPath(c, id), m, nil)
		}
		return c.Post(ctx, k.BasePath(c), m, nil)
	}
	return k
}

// OrgCreated is the server's reply to creating an organisation. PrivateKey
// is the new NAME-validator client's key and is not retrievable later.
type OrgCreated struct {
	ClientName string `json:"clientname"`
	PrivateKey string `json:"private_key"`
	URI        string `json:"uri"`
}

// CreateOrganization creates an organisation (superuser only).
func CreateOrganization(ctx context.Context, c *chef.Client, name, fullName string) (*OrgCreated, error) {
	if fullName == "" {
		fullName = name
	}
	var out OrgCreated
	if err := c.Post(ctx, Organization.BasePath(c), map[string]string{"name": name, "full_name": fullName}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SaveValidatorKey writes a validator private key to path (0600), refusing
// to overwrite. Callers should check ValidatorKeyExists before creating the
// organisation so the key is never lost.
func SaveValidatorKey(path, key string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(key); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// filesKind is the shape of cookbooks and cookbook artifacts: composite,
// directory-based. Put is nil; the cookbook package transfers them.
func filesKind(name, plural, subLabel string, order int) *Kind {
	k := &Kind{Name: name, Plural: plural, Composite: true, SubLabel: subLabel, Order: order, Files: true}
	k.List = func(ctx context.Context, c *chef.Client) ([]ID, error) {
		var m map[string]struct {
			Versions []struct {
				Version    string `json:"version"`
				Identifier string `json:"identifier"`
			} `json:"versions"`
		}
		if err := c.Get(ctx, k.BasePath(c)+"?num_versions=all", &m); err != nil {
			return nil, err
		}
		var ids []ID
		for n, e := range m {
			for _, v := range e.Versions {
				sub := v.Version
				if sub == "" {
					sub = v.Identifier
				}
				ids = append(ids, ID{Name: n, Sub: sub})
			}
		}
		sortIDs(ids)
		return ids, nil
	}
	k.Get = func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error) {
		if id.Sub == "" {
			return getRaw(ctx, c, k.BasePath(c)+"/"+url.PathEscape(id.Name)+"?num_versions=all")
		}
		return getRaw(ctx, c, k.ObjectPath(c, id))
	}
	k.Delete = func(ctx context.Context, c *chef.Client, id ID) error {
		if id.Sub == "" {
			return fmt.Errorf("%s %s: %s required", name, id.Name, subLabel)
		}
		return c.Delete(ctx, k.ObjectPath(c, id)+"?purge=true", nil)
	}
	return k
}

func dataBagKind() *Kind {
	k := simpleJSON("databag", "data_bags", OrderDataBag, false)
	k.BasePathOverride("/data")
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if exists {
			return nil
		}
		return c.Post(ctx, k.BasePath(c), map[string]string{"name": id.Name}, nil)
	}
	return k
}

func dataBagItemKind() *Kind {
	k := &Kind{Name: "databag-item", Plural: "data_bags", Composite: true, SubLabel: "item", Order: OrderDataBagItem}
	k.BasePathOverride("/data")
	k.List = func(ctx context.Context, c *chef.Client) ([]ID, error) {
		bags, err := DataBag.List(ctx, c)
		if err != nil {
			return nil, err
		}
		var ids []ID
		for _, b := range bags {
			items, err := listMap(ctx, c, k.BasePath(c)+"/"+url.PathEscape(b.Name))
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				ids = append(ids, ID{Name: b.Name, Sub: it.Name})
			}
		}
		sortIDs(ids)
		return ids, nil
	}
	k.Get = func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error) {
		if id.Sub == "" {
			return getRaw(ctx, c, k.BasePath(c)+"/"+url.PathEscape(id.Name))
		}
		return getRaw(ctx, c, k.ObjectPath(c, id))
	}
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		body, err := SetField(body, "id", id.Sub)
		if err != nil {
			return err
		}
		if exists {
			return c.Put(ctx, k.ObjectPath(c, id), body, nil)
		}
		return c.Post(ctx, k.BasePath(c)+"/"+url.PathEscape(id.Name), body, nil)
	}
	k.Delete = func(ctx context.Context, c *chef.Client, id ID) error {
		return c.Delete(ctx, k.ObjectPath(c, id), nil)
	}
	return k
}

// BasePathOverride sets a collection path that differs from the plural
// (data bags live under /data).
func (k *Kind) BasePathOverride(p string) {
	k.basePath = p
}

func policyKind() *Kind {
	k := &Kind{Name: "policy", Plural: "policies", Composite: true, SubLabel: "revision", Order: OrderPolicy}
	k.List = func(ctx context.Context, c *chef.Client) ([]ID, error) {
		var m map[string]struct {
			Revisions map[string]json.RawMessage `json:"revisions"`
		}
		if err := c.Get(ctx, k.BasePath(c), &m); err != nil {
			return nil, err
		}
		var ids []ID
		for n, e := range m {
			for r := range e.Revisions {
				ids = append(ids, ID{Name: n, Sub: r})
			}
		}
		sortIDs(ids)
		return ids, nil
	}
	k.Get = func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error) {
		if id.Sub == "" {
			return getRaw(ctx, c, k.BasePath(c)+"/"+url.PathEscape(id.Name))
		}
		return getRaw(ctx, c, k.revisionPath(c, id))
	}
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if exists {
			return nil // revisions are immutable and content-addressed
		}
		err := c.Post(ctx, k.BasePath(c)+"/"+url.PathEscape(id.Name)+"/revisions", body, nil)
		if chef.IsConflict(err) {
			return nil
		}
		return err
	}
	k.Delete = func(ctx context.Context, c *chef.Client, id ID) error {
		if id.Sub == "" {
			return c.Delete(ctx, k.BasePath(c)+"/"+url.PathEscape(id.Name), nil)
		}
		return c.Delete(ctx, k.revisionPath(c, id), nil)
	}
	return k
}

func (k *Kind) revisionPath(c *chef.Client, id ID) string {
	return k.BasePath(c) + "/" + url.PathEscape(id.Name) + "/revisions/" + url.PathEscape(id.Sub)
}

// PolicyGroupPolicies extracts policy name -> revision from a policy group body.
func PolicyGroupPolicies(body json.RawMessage) map[string]string {
	var g struct {
		Policies map[string]struct {
			RevisionID string `json:"revision_id"`
		} `json:"policies"`
	}
	_ = json.Unmarshal(body, &g)
	out := map[string]string{}
	for n, p := range g.Policies {
		out[n] = p.RevisionID
	}
	return out
}

func policyGroupKind() *Kind {
	k := simpleJSON("policygroup", "policy_groups", OrderPolicyGroup, false)
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		// A group is the set of policy revisions assigned to it; assigning
		// needs the full revision document, which must already be on this server.
		for name, rev := range PolicyGroupPolicies(body) {
			doc, err := Policy.Get(ctx, c, ID{Name: name, Sub: rev})
			if err != nil {
				return fmt.Errorf("policy %s/%s must exist before assigning it to group %s: %w", name, rev, id.Name, err)
			}
			if err := c.Put(ctx, k.ObjectPath(c, id)+"/policies/"+url.PathEscape(name), doc, nil); err != nil {
				return err
			}
		}
		return nil
	}
	return k
}
