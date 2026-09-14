// Package acl reads and writes object ACLs.
package acl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// Perms are the five ACL permissions in the order the server lists them.
var Perms = []string{"create", "read", "update", "delete", "grant"}

// Perm is the actor set for one permission. The server returns actors as
// a flat list and, since API v1, also split into users and clients.
type Perm struct {
	Actors  []string `json:"actors"`
	Groups  []string `json:"groups"`
	Users   []string `json:"users,omitempty"`
	Clients []string `json:"clients,omitempty"`
}

// ACL maps permission -> Perm.
type ACL map[string]Perm

// Kinds that carry ACLs, in the order they are backed up and restored.
var Kinds = []*kinds.Kind{
	kinds.Container, kinds.Group, kinds.Client, kinds.Environment, kinds.Cookbook,
	kinds.Artifact, kinds.Role, kinds.DataBag, kinds.Node, kinds.Policy, kinds.PolicyGroup,
}

// Path is the _acl path for an object; cookbook and policy ACLs are per
// name, not per version.
func Path(c *chef.Client, k *kinds.Kind, name string) string {
	return k.BasePath(c) + "/" + url.PathEscape(name) + "/_acl"
}

// OrgPath is the organisation's own ACL.
func OrgPath(c *chef.Client) string { return c.OrgPath("/organizations/_acl") }

// Get fetches an ACL.
func Get(ctx context.Context, c *chef.Client, path string) (ACL, error) {
	var a ACL
	if err := c.Get(ctx, path, &a); err != nil {
		return nil, err
	}
	return a, nil
}

// Put writes every permission of the ACL. When users/clients are present
// they are sent instead of actors (the server rejects both together).
func Put(ctx context.Context, c *chef.Client, path string, a ACL) error {
	for _, perm := range Perms {
		p, ok := a[perm]
		if !ok {
			continue
		}
		body := map[string]any{}
		entry := map[string]any{"groups": nonNil(p.Groups)}
		if p.Users != nil || p.Clients != nil {
			entry["users"] = nonNil(p.Users)
			entry["clients"] = nonNil(p.Clients)
		} else {
			entry["actors"] = nonNil(p.Actors)
		}
		body[perm] = entry
		if err := c.Put(ctx, path+"/"+perm, body, nil); err != nil {
			return fmt.Errorf("%s: %w", perm, err)
		}
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Filter drops actors and groups that keep returns false for, so an ACL can
// be applied on a server that lacks some of the source's principals. It
// returns the names dropped.
func Filter(a ACL, keep func(kind, name string) bool) (ACL, []string) {
	out := ACL{}
	dropped := map[string]bool{}
	filter := func(kind string, in []string) []string {
		res := []string{}
		for _, n := range in {
			if keep(kind, n) {
				res = append(res, n)
			} else {
				dropped[kind+":"+n] = true
			}
		}
		return res
	}
	for perm, p := range a {
		np := Perm{Groups: filter("group", p.Groups)}
		if p.Users != nil || p.Clients != nil {
			np.Users = filter("user", p.Users)
			np.Clients = filter("client", p.Clients)
		} else {
			np.Actors = filter("actor", p.Actors)
		}
		out[perm] = np
	}
	names := make([]string, 0, len(dropped))
	for n := range dropped {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, names
}

// Normalize fills users/clients from actors when only actors are known, so
// the write form is stable. Names present in clientNames are clients;
// everything else is a user.
func Normalize(a ACL, clientNames map[string]bool) ACL {
	out := ACL{}
	for perm, p := range a {
		if p.Users == nil && p.Clients == nil {
			p.Users, p.Clients = []string{}, []string{}
			for _, n := range p.Actors {
				if clientNames[n] {
					p.Clients = append(p.Clients, n)
				} else {
					p.Users = append(p.Users, n)
				}
			}
		}
		out[perm] = p
	}
	return out
}

// MarshalIndent renders an ACL for storage.
func MarshalIndent(a ACL) ([]byte, error) {
	ordered := make(map[string]Perm, len(a))
	for k, v := range a {
		ordered[k] = v
	}
	return json.MarshalIndent(ordered, "", "  ")
}
