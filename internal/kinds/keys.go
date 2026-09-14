package kinds

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"

	"github.com/trickyearlobe/gnife/internal/chef"
)

// Key is one public key of a client or user.
type Key struct {
	Name           string `json:"name"`
	PublicKey      string `json:"public_key"`
	ExpirationDate string `json:"expiration_date"`
}

// ListKeys fetches every key of a client or user, sorted by name.
func ListKeys(ctx context.Context, c *chef.Client, k *Kind, name string) ([]Key, error) {
	base := k.ObjectPath(c, ID{Name: name}) + "/keys"
	var list []struct {
		Name string `json:"name"`
	}
	if err := c.Get(ctx, base, &list); err != nil {
		return nil, err
	}
	keys := make([]Key, 0, len(list))
	for _, e := range list {
		var key Key
		if err := c.Get(ctx, base+"/"+url.PathEscape(e.Name), &key); err != nil {
			return nil, err
		}
		if key.ExpirationDate == "" {
			key.ExpirationDate = "infinity"
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys, nil
}

// PutKeys makes the object's keys match keys: missing ones are added,
// existing ones updated. Nothing is deleted.
func PutKeys(ctx context.Context, c *chef.Client, k *Kind, name string, keys []Key) error {
	base := k.ObjectPath(c, ID{Name: name}) + "/keys"
	for _, key := range keys {
		if key.PublicKey == "" {
			continue
		}
		body := map[string]any{"name": key.Name, "public_key": key.PublicKey, "expiration_date": key.ExpirationDate}
		if body["expiration_date"] == "" {
			body["expiration_date"] = "infinity"
		}
		err := c.Put(ctx, base+"/"+url.PathEscape(key.Name), body, nil)
		if chef.IsNotFound(err) {
			err = c.Post(ctx, base, body, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// HasExtraKeys reports whether the set holds anything beyond a single
// default key (the only key the knife-ec-backup layout can carry).
func HasExtraKeys(keys []Key) bool {
	return len(keys) > 1 || len(keys) == 1 && keys[0].Name != "default"
}

// KeysJSON renders keys for a sidecar file.
func KeysJSON(keys []Key) json.RawMessage {
	b, _ := json.Marshal(keys)
	return b
}
