package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
	"github.com/trickyearlobe/gnife/internal/transfer"
)

// DirSource reads an organisation directory as a transfer.Source.
type DirSource struct {
	OrgDir string
}

func (d *DirSource) Name() string { return d.OrgDir }

func (d *DirSource) Get(ctx context.Context, k *kinds.Kind, id kinds.ID) (json.RawMessage, error) {
	if k == kinds.DataBag {
		return json.RawMessage(`{}`), nil
	}
	p, err := ObjectPath(d.OrgDir, k, id)
	if err != nil {
		return nil, err
	}
	body, err := readJSON(p)
	if err != nil {
		return nil, err
	}
	if k == kinds.Group {
		body = kinds.DropFields(body, "orgname") // the on-disk org may have been renamed
	}
	return body, nil
}

func (d *DirSource) Cookbook(ctx context.Context, k *kinds.Kind, id kinds.ID) (*cookbook.Manifest, transfer.FileReader, error) {
	dir, err := ObjectPath(d.OrgDir, k, id)
	if err != nil {
		return nil, nil, err
	}
	m, paths, err := cookbook.LoadDir(k, id, dir)
	if err != nil {
		return nil, nil, err
	}
	return m, func(_ context.Context, sum string) ([]byte, error) {
		p, ok := paths[sum]
		if !ok {
			return nil, fmt.Errorf("no file with checksum %s in %s", sum, dir)
		}
		return os.ReadFile(p)
	}, nil
}

func (d *DirSource) ACL(ctx context.Context, k *kinds.Kind, name string) (acl.ACL, error) {
	p, err := ACLPath(d.OrgDir, k, name)
	if err != nil {
		return nil, err
	}
	body, err := readJSON(p)
	if err != nil {
		return nil, err
	}
	var a acl.ACL
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return a, nil
}

// Items lists everything in the organisation directory as plan items.
func (d *DirSource) Items() ([]transfer.Item, error) {
	var items []transfer.Item
	add := func(k *kinds.Kind, id kinds.ID) {
		items = append(items, transfer.Item{Kind: k, ID: id, Reason: "backup"})
	}
	for _, k := range JSONKinds {
		switch k {
		case kinds.DataBagItem:
			bags, err := listDirs(filepath.Join(d.OrgDir, "data_bags"))
			if err != nil {
				return nil, err
			}
			for _, bag := range bags {
				add(kinds.DataBag, kinds.ID{Name: bag})
				names, err := listJSONNames(filepath.Join(d.OrgDir, "data_bags", bag))
				if err != nil {
					return nil, err
				}
				for _, n := range names {
					add(kinds.DataBagItem, kinds.ID{Name: bag, Sub: n})
				}
			}
		case kinds.Policy:
			names, err := listJSONNames(filepath.Join(d.OrgDir, "policies"))
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				id, ok := cookbook.ParseDirName(n)
				if !ok {
					return nil, fmt.Errorf("policies/%s.json: expected NAME-REVISION", n)
				}
				add(kinds.Policy, id)
			}
		default:
			names, err := listJSONNames(filepath.Join(d.OrgDir, k.Plural))
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				add(k, kinds.ID{Name: n})
			}
		}
	}
	for _, k := range FilesKinds {
		dirs, err := listDirs(filepath.Join(d.OrgDir, k.Plural))
		if err != nil {
			return nil, err
		}
		for _, dir := range dirs {
			id, ok := cookbook.ParseDirName(dir)
			if !ok {
				return nil, fmt.Errorf("%s/%s: expected NAME-%s", k.Plural, dir, strings.ToUpper(k.SubLabel))
			}
			add(k, id)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Kind.Order != b.Kind.Order {
			return a.Kind.Order < b.Kind.Order
		}
		if a.ID.Name != b.ID.Name {
			return a.ID.Name < b.ID.Name
		}
		return a.ID.Sub < b.ID.Sub
	})
	return items, nil
}

// ACLItems lists the ACL files present.
func (d *DirSource) ACLItems() ([]transfer.ACLItem, error) {
	var out []transfer.ACLItem
	if _, err := os.Stat(filepath.Join(d.OrgDir, "acls", "organization.json")); err == nil {
		out = append(out, transfer.ACLItem{Kind: nil})
	}
	for _, k := range acl.Kinds {
		names, err := listJSONNames(filepath.Join(d.OrgDir, "acls", k.Plural))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, transfer.ACLItem{Kind: k, Name: n})
		}
	}
	return out, nil
}
