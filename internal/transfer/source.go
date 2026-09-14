// Package transfer moves objects into a destination organisation from a
// Source — another organisation (copy, clone) or a backup on disk (restore).
package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/trickyearlobe/gnife/internal/acl"
	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// FileReader returns the content for a cookbook file checksum.
type FileReader func(ctx context.Context, checksum string) ([]byte, error)

// Source supplies object bodies to Execute.
type Source interface {
	// Name describes the source in messages.
	Name() string
	// Get returns an object body.
	Get(ctx context.Context, k *kinds.Kind, id kinds.ID) (json.RawMessage, error)
	// Cookbook returns a manifest and a reader for its files.
	Cookbook(ctx context.Context, k *kinds.Kind, id kinds.ID) (*cookbook.Manifest, FileReader, error)
	// ACL returns an object's ACL; k == nil means the organisation ACL.
	ACL(ctx context.Context, k *kinds.Kind, name string) (acl.ACL, error)
}

// ServerSource reads from a live organisation.
type ServerSource struct {
	Client *chef.Client
	mu     sync.Mutex
	cache  map[string][]byte
}

// NewServerSource wraps a client.
func NewServerSource(c *chef.Client) *ServerSource {
	return &ServerSource{Client: c, cache: map[string][]byte{}}
}

func (s *ServerSource) Name() string {
	return s.Client.ServerRoot() + " org " + s.Client.Org()
}

func (s *ServerSource) Get(ctx context.Context, k *kinds.Kind, id kinds.ID) (json.RawMessage, error) {
	return k.Get(ctx, s.Client, id)
}

func (s *ServerSource) Cookbook(ctx context.Context, k *kinds.Kind, id kinds.ID) (*cookbook.Manifest, FileReader, error) {
	m, err := cookbook.Fetch(ctx, s.Client, k, id)
	if err != nil {
		return nil, nil, err
	}
	urls := map[string]string{}
	for _, f := range m.Files {
		urls[f.Checksum] = f.URL
	}
	read := func(ctx context.Context, sum string) ([]byte, error) {
		s.mu.Lock()
		data, ok := s.cache[sum]
		s.mu.Unlock()
		if ok {
			return data, nil
		}
		u := urls[sum]
		if u == "" {
			return nil, fmt.Errorf("no source URL for checksum %s", sum)
		}
		data, err := s.Client.Fetch(ctx, u)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.cache[sum] = data
		s.mu.Unlock()
		return data, nil
	}
	return m, read, nil
}

func (s *ServerSource) ACL(ctx context.Context, k *kinds.Kind, name string) (acl.ACL, error) {
	if k == nil {
		return acl.Get(ctx, s.Client, acl.OrgPath(s.Client))
	}
	return acl.Get(ctx, s.Client, acl.Path(s.Client, k, name))
}
