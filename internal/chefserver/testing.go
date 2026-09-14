package chefserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"sort"
	"strings"
)

// Test helpers: populate a server directly.

// AddClient registers an API client with a fresh key and returns its PEM.
func (s *Server) AddClient(org, name string, admin bool) []byte {
	priv, pub, err := generateKeyPair()
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	o := s.newOrg(org, org)
	o.Clients[name], _ = json.Marshal(map[string]any{"name": name, "clientname": name, "validator": false, "orgname": org, "json_class": "Chef::ApiClient", "chef_type": "client"})
	o.ClientKeys[name] = map[string]string{"default": pub}
	o.Groups["clients"].Clients = appendUnique(o.Groups["clients"].Clients, name)
	if admin {
		o.Groups["admins"].Clients = appendUnique(o.Groups["admins"].Clients, name)
	}
	s.mu.Unlock()
	return priv
}

// AddUser registers a server-level user (superuser if asked) and returns its PEM.
func (s *Server) AddUser(name string, super bool) []byte {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	body, _ := json.Marshal(map[string]any{"username": name, "display_name": name, "email": name + "@example.com"})
	s.mu.Lock()
	s.Users[name] = &User{Body: body, PublicKey: pubPEM, Keys: map[string]string{}}
	if super {
		s.Superusers[name] = true
	}
	s.mu.Unlock()
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// AddCookbook stores a cookbook version with the given files and metadata.
func (s *Server) AddCookbook(org, name, version string, files map[string]string, meta map[string]any) {
	s.addFiles(org, name, version, files, meta, false)
}

// AddArtifact stores a cookbook artifact.
func (s *Server) AddArtifact(org, name, identifier string, files map[string]string, meta map[string]any) {
	s.addFiles(org, name, identifier, files, meta, true)
}

func (s *Server) addFiles(org, name, sub string, files map[string]string, meta map[string]any, artifact bool) {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["name"] = name
	if !artifact {
		meta["version"] = sub
	}
	var all []map[string]any
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		content := files[p]
		sum := checksum([]byte(content))
		s.Bookshelf[sum] = []byte(content)
		parts := strings.Split(p, "/")
		entryName := "root_files/" + p
		if len(parts) > 1 {
			entryName = parts[0] + "/" + parts[len(parts)-1]
		}
		all = append(all, map[string]any{"name": entryName, "path": p, "checksum": sum, "specificity": "default"})
	}
	body := map[string]any{"name": name + "-" + sub, "metadata": meta, "all_files": all, "frozen?": false, "chef_type": "cookbook_version"}
	if artifact {
		body["name"] = name
		body["identifier"] = sub
		body["version"] = meta["version"]
	} else {
		body["cookbook_name"] = name
		body["version"] = sub
		body["json_class"] = "Chef::CookbookVersion"
	}
	raw, _ := json.Marshal(body)
	o := s.newOrg(org, org)
	store := o.Cookbooks
	if artifact {
		store = o.Artifacts
	}
	if store[name] == nil {
		store[name] = map[string]json.RawMessage{}
	}
	store[name][sub] = raw
}
