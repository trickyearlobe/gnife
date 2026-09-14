// Package kinds is the registry of Chef object types. Each Kind knows how to
// list, fetch, write and delete its objects, so the CLI verbs, backup,
// restore and copy are written once and apply to every type.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/trickyearlobe/gnife/internal/chef"
)

// ID identifies one object. Sub is set for composite kinds: a cookbook
// version, a policy revision, a data bag item, an artifact identifier.
type ID struct {
	Name string
	Sub  string
}

func (id ID) String() string {
	if id.Sub == "" {
		return id.Name
	}
	return id.Name + "/" + id.Sub
}

// Kind describes one object type.
type Kind struct {
	// Name is the singular CLI noun and registry key: "node".
	Name string
	// Plural is the API path segment and knife-ec-backup directory: "nodes".
	Plural string
	// ServerLevel kinds live at the server root rather than under an organisation.
	ServerLevel bool
	// Composite kinds have a two-part ID; SubLabel names the second part.
	Composite bool
	SubLabel  string
	// Order is the restore order (lower first). Zero means "not restorable
	// through the generic path" (cookbooks, ACLs are handled specially).
	Order int
	// Files kinds (cookbooks, artifacts) transfer a directory of files
	// rather than a single JSON body; Put is nil for them.
	Files bool

	// basePath overrides "/"+Plural for the collection path (data bags: /data).
	basePath string

	List   func(ctx context.Context, c *chef.Client) ([]ID, error)
	Get    func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error)
	Put    func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error
	Delete func(ctx context.Context, c *chef.Client, id ID) error
}

// BasePath is the collection path for the kind on the given client.
func (k *Kind) BasePath(c *chef.Client) string {
	p := "/" + k.Plural
	if k.basePath != "" {
		p = k.basePath
	}
	if k.ServerLevel {
		return p
	}
	return c.OrgPath(p)
}

// ObjectPath is the path of one object.
func (k *Kind) ObjectPath(c *chef.Client, id ID) string {
	p := k.BasePath(c) + "/" + url.PathEscape(id.Name)
	if k.Composite && id.Sub != "" {
		p += "/" + url.PathEscape(id.Sub)
	}
	return p
}

// Exists reports whether the object is present, treating 404 as false.
func (k *Kind) Exists(ctx context.Context, c *chef.Client, id ID) (bool, error) {
	_, err := k.Get(ctx, c, id)
	if err == nil {
		return true, nil
	}
	if chef.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// ParseID turns CLI arguments into an ID for the kind.
func (k *Kind) ParseID(args []string) (ID, error) {
	switch {
	case len(args) == 0:
		return ID{}, fmt.Errorf("%s: name required", k.Name)
	case !k.Composite:
		if len(args) > 1 {
			return ID{}, fmt.Errorf("%s: expected one name, got %d arguments", k.Name, len(args))
		}
		return ID{Name: args[0]}, nil
	case len(args) == 1:
		if name, sub, ok := strings.Cut(args[0], "/"); ok {
			return ID{Name: name, Sub: sub}, nil
		}
		return ID{Name: args[0]}, nil
	case len(args) == 2:
		return ID{Name: args[0], Sub: args[1]}, nil
	}
	return ID{}, fmt.Errorf("%s: expected NAME %s, got %d arguments", k.Name, strings.ToUpper(k.SubLabel), len(args))
}

var registry = map[string]*Kind{}
var order []string

func register(k *Kind) *Kind {
	registry[k.Name] = k
	order = append(order, k.Name)
	return k
}

// Get returns a registered kind by singular name or plural/backup name.
func Get(name string) (*Kind, bool) {
	if k, ok := registry[name]; ok {
		return k, true
	}
	for _, k := range registry {
		if k.Plural == name {
			return k, true
		}
	}
	return nil, false
}

// All returns every kind in registration order.
func All() []*Kind {
	out := make([]*Kind, 0, len(order))
	for _, n := range order {
		out = append(out, registry[n])
	}
	return out
}

// Restorable returns the kinds with a restore order, sorted by it.
func Restorable() []*Kind {
	var out []*Kind
	for _, k := range All() {
		if k.Order > 0 {
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// Names returns the registered singular names in order.
func Names() []string {
	return append([]string(nil), order...)
}

// listMap lists a "name": "url" collection response.
func listMap(ctx context.Context, c *chef.Client, path string) ([]ID, error) {
	var m map[string]json.RawMessage
	if err := c.Get(ctx, path, &m); err != nil {
		return nil, err
	}
	ids := make([]ID, 0, len(m))
	for name := range m {
		ids = append(ids, ID{Name: name})
	}
	sortIDs(ids)
	return ids, nil
}

func sortIDs(ids []ID) {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].Name != ids[j].Name {
			return ids[i].Name < ids[j].Name
		}
		return ids[i].Sub < ids[j].Sub
	})
}

// getRaw fetches an object body verbatim.
func getRaw(ctx context.Context, c *chef.Client, path string) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// simpleJSON builds a Kind for the common "POST to create, PUT to replace"
// object types.
func simpleJSON(name, plural string, order int, serverLevel bool) *Kind {
	k := &Kind{Name: name, Plural: plural, Order: order, ServerLevel: serverLevel}
	k.List = func(ctx context.Context, c *chef.Client) ([]ID, error) {
		return listMap(ctx, c, k.BasePath(c))
	}
	k.Get = func(ctx context.Context, c *chef.Client, id ID) (json.RawMessage, error) {
		return getRaw(ctx, c, k.ObjectPath(c, id))
	}
	k.Put = func(ctx context.Context, c *chef.Client, id ID, body json.RawMessage, exists bool) error {
		if exists {
			return c.Put(ctx, k.ObjectPath(c, id), body, nil)
		}
		return c.Post(ctx, k.BasePath(c), body, nil)
	}
	k.Delete = func(ctx context.Context, c *chef.Client, id ID) error {
		return c.Delete(ctx, k.ObjectPath(c, id), nil)
	}
	return k
}

// SetField returns body with one top-level field replaced.
func SetField(body json.RawMessage, key string, value any) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m[key] = value
	return json.Marshal(m)
}

// DropFields returns body without the named top-level fields.
func DropFields(body json.RawMessage, keys ...string) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	for _, k := range keys {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// Field returns a top-level string field of body, or "".
func Field(body json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return ""
	}
	return s
}

// NameOf returns the object's own name from its body ("name", "id",
// "groupname", "containername", "username", falling back to the ID).
func NameOf(body json.RawMessage, id ID) string {
	for _, k := range []string{"name", "id", "groupname", "containername", "username"} {
		if v := Field(body, k); v != "" {
			return v
		}
	}
	return id.Name
}
