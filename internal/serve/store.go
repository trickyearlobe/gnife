// Package serve runs the Chef server implementation over a directory in the
// knife-ec-backup layout: a backup becomes a live organisation, and every
// change is written straight back to disk.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trickyearlobe/gnife/internal/backup"
	"github.com/trickyearlobe/gnife/internal/chefserver"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// DirStore persists to a backup directory.
type DirStore struct {
	Root string
}

func (d *DirStore) orgDir(org string) string { return backup.OrgDir(d.Root, org) }

func (d *DirStore) PutObject(org string, k *kinds.Kind, id kinds.ID, body json.RawMessage) error {
	p, err := backup.ObjectPath(d.orgDir(org), k, id)
	if err != nil {
		return err
	}
	if k == kinds.DataBag {
		return os.MkdirAll(p, 0o755)
	}
	if k == kinds.Container {
		body, _ = json.Marshal(map[string]string{"containername": id.Name, "containerpath": id.Name})
	}
	return backup.WriteJSON(p, body)
}

func (d *DirStore) DeleteObject(org string, k *kinds.Kind, id kinds.ID) error {
	p, err := backup.ObjectPath(d.orgDir(org), k, id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The ACL file goes with the object (per name for versioned kinds).
	if id.Sub == "" || !k.Composite || k == kinds.DataBagItem {
		if ap, err := backup.ACLPath(d.orgDir(org), k, id.Name); err == nil && k != kinds.DataBagItem {
			_ = os.Remove(ap)
		}
	}
	return nil
}

func (d *DirStore) PutCookbook(org string, k *kinds.Kind, id kinds.ID, manifest json.RawMessage, files map[string][]byte) error {
	dir, err := backup.ObjectPath(d.orgDir(org), k, id)
	if err != nil {
		return err
	}
	m, err := cookbook.Parse(k, id, manifest)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	hasMetadata := false
	for _, f := range m.Files {
		rel, err := cookbook.SafeRelPath(f.Path)
		if err != nil {
			return err
		}
		if rel == "metadata.json" {
			hasMetadata = true
		}
		data, ok := files[f.Checksum]
		if !ok {
			return fmt.Errorf("no content for %s (%s)", f.Path, f.Checksum)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	if !hasMetadata {
		if err := backup.WriteJSON(filepath.Join(dir, "metadata.json"), m.Metadata); err != nil {
			return err
		}
	}
	if m.Frozen {
		return os.WriteFile(filepath.Join(dir, ".frozen"), nil, 0o644)
	}
	return nil
}

func (d *DirStore) PutACL(org, key string, acl map[string]chefserver.Perm) error {
	var p string
	var err error
	if key == "organization" {
		p, err = backup.ACLPath(d.orgDir(org), nil, "")
	} else {
		plural, name, _ := splitKey(key)
		k, ok := kinds.Get(plural)
		if !ok {
			return fmt.Errorf("unknown acl kind %q", plural)
		}
		p, err = backup.ACLPath(d.orgDir(org), k, name)
	}
	if err != nil {
		return err
	}
	return backup.WriteJSON(p, acl)
}

func (d *DirStore) PutUser(name string, body json.RawMessage, acl map[string]chefserver.Perm) error {
	if err := backup.SafeName(name); err != nil {
		return err
	}
	if err := backup.WriteJSON(filepath.Join(d.Root, "users", name+".json"), body); err != nil {
		return err
	}
	if acl != nil {
		return backup.WriteJSON(filepath.Join(d.Root, "user_acls", name+".json"), acl)
	}
	return nil
}

func (d *DirStore) DeleteUser(name string) error {
	if err := backup.SafeName(name); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(d.Root, "user_acls", name+".json"))
	err := os.Remove(filepath.Join(d.Root, "users", name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (d *DirStore) PutKeys(org string, k *kinds.Kind, name string, keys []kinds.Key) error {
	p, err := backup.KeysPath(d.Root, d.orgDir(org), k, name)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		err := os.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return backup.WriteJSON(p, kinds.KeysJSON(keys))
}

func (d *DirStore) PutOrg(name, fullName string, members []string) error {
	if err := backup.SafeName(name); err != nil {
		return err
	}
	dir := d.orgDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := backup.WriteJSON(filepath.Join(dir, "org.json"), map[string]string{"name": name, "full_name": fullName}); err != nil {
		return err
	}
	list := make([]map[string]map[string]string, 0, len(members))
	for _, m := range members {
		list = append(list, map[string]map[string]string{"user": {"username": m}})
	}
	return backup.WriteJSON(filepath.Join(dir, "members.json"), list)
}

func (d *DirStore) DeleteOrg(name string) error {
	if err := backup.SafeName(name); err != nil {
		return err
	}
	return os.RemoveAll(d.orgDir(name))
}

func splitKey(key string) (plural, name string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

// Load reads every organisation and user under Root into the server.
func (d *DirStore) Load(s *chefserver.Server) error {
	orgs, err := os.ReadDir(filepath.Join(d.Root, "organizations"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range orgs {
		if !e.IsDir() {
			continue
		}
		if err := d.loadOrg(s, e.Name()); err != nil {
			return fmt.Errorf("organization %s: %w", e.Name(), err)
		}
	}
	users, _ := os.ReadDir(filepath.Join(d.Root, "users"))
	for _, e := range users {
		name, ok := jsonName(e)
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(d.Root, "users", e.Name()))
		if err != nil {
			return err
		}
		var acl map[string]chefserver.Perm
		if raw, err := os.ReadFile(filepath.Join(d.Root, "user_acls", e.Name())); err == nil {
			_ = json.Unmarshal(raw, &acl)
		}
		s.SetUser(name, body, acl)
		if keys, err := readKeys(filepath.Join(d.Root, "user_keys", e.Name())); err == nil && keys != nil {
			s.SetKeys("", kinds.User, name, keys)
		}
	}
	return nil
}

func readKeys(path string) ([]kinds.Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []kinds.Key
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys, nil
}

func jsonName(e fs.DirEntry) (string, bool) {
	n := e.Name()
	if e.IsDir() || len(n) < 6 || n[len(n)-5:] != ".json" || n[0] == '.' {
		return "", false
	}
	return n[:len(n)-5], true
}

func (d *DirStore) loadOrg(s *chefserver.Server, org string) error {
	dir := d.orgDir(org)
	full := org
	var members []string
	if raw, err := os.ReadFile(filepath.Join(dir, "org.json")); err == nil {
		if f := kinds.Field(raw, "full_name"); f != "" {
			full = f
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "members.json")); err == nil {
		var list []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		_ = json.Unmarshal(raw, &list)
		for _, m := range list {
			if m.User.Username != "" {
				members = append(members, m.User.Username)
			}
		}
	}
	s.SetOrg(org, full, members)

	src := &backup.DirSource{OrgDir: dir}
	items, err := src.Items()
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Kind.Files {
			m, read, err := src.Cookbook(context.Background(), it.Kind, it.ID)
			if err != nil {
				return fmt.Errorf("%s %s: %w", it.Kind.Name, it.ID, err)
			}
			files := map[string][]byte{}
			for _, f := range m.Files {
				data, err := read(context.Background(), f.Checksum)
				if err != nil {
					return err
				}
				files[f.Checksum] = data
			}
			manifest, _ := json.Marshal(m.Body(false))
			cbDir, _ := backup.ObjectPath(dir, it.Kind, it.ID)
			_, frozenErr := os.Stat(filepath.Join(cbDir, ".frozen"))
			s.SetCookbook(org, it.Kind, it.ID, manifest, files, frozenErr == nil)
			continue
		}
		body, err := src.Get(context.Background(), it.Kind, it.ID)
		if err != nil {
			return fmt.Errorf("%s %s: %w", it.Kind.Name, it.ID, err)
		}
		if err := s.SetObject(org, it.Kind, it.ID, body); err != nil {
			return err
		}
		if it.Kind == kinds.Client {
			if p, err := backup.KeysPath(d.Root, dir, kinds.Client, it.ID.Name); err == nil {
				if keys, err := readKeys(p); err == nil && keys != nil {
					s.SetKeys(org, kinds.Client, it.ID.Name, keys)
				}
			}
		}
	}
	acls, err := src.ACLItems()
	if err != nil {
		return err
	}
	for _, a := range acls {
		got, err := src.ACL(context.Background(), a.Kind, a.Name)
		if err != nil {
			return err
		}
		perms := map[string]chefserver.Perm{}
		for perm, p := range got {
			actors := p.Actors
			if p.Users != nil || p.Clients != nil {
				actors = append(append([]string{}, p.Users...), p.Clients...)
			}
			perms[perm] = chefserver.Perm{Actors: actors, Groups: p.Groups}
		}
		key := "organization"
		if a.Kind != nil {
			key = a.Kind.Plural + "/" + a.Name
		}
		s.SetACL(org, key, perms)
	}
	return nil
}
