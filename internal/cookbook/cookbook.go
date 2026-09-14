// Package cookbook transfers cookbooks and cookbook artifacts: the JSON
// manifest plus every file it references, via bookshelf URLs and sandboxes.
package cookbook

import (
	"context"
	"crypto/md5" // #nosec G501 -- Chef identifies cookbook files by MD5; not used for security
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// File is one entry of a cookbook manifest.
type File struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Checksum    string `json:"checksum"`
	Specificity string `json:"specificity"`
	URL         string `json:"url,omitempty"`
}

// Manifest is a parsed cookbook or artifact version.
type Manifest struct {
	Kind     *kinds.Kind
	ID       kinds.ID // Name + version or identifier
	Frozen   bool
	Metadata json.RawMessage
	Files    []File
	// Extra keeps other top-level fields (version on artifacts, etc.).
	Extra map[string]json.RawMessage
}

var segments = map[string]bool{
	"attributes": true, "definitions": true, "files": true, "libraries": true,
	"providers": true, "recipes": true, "resources": true, "templates": true,
}

// Parse decodes a manifest in the API v2 (all_files) or v0/v1 (segments) form.
func Parse(k *kinds.Kind, id kinds.ID, body json.RawMessage) (*Manifest, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("%s %s: decoding manifest: %w", k.Name, id, err)
	}
	out := &Manifest{Kind: k, ID: id, Extra: map[string]json.RawMessage{}}
	if raw, ok := m["all_files"]; ok {
		if err := json.Unmarshal(raw, &out.Files); err != nil {
			return nil, fmt.Errorf("%s %s: decoding all_files: %w", k.Name, id, err)
		}
	} else {
		for seg := range segments {
			var fs []File
			if raw, ok := m[seg]; ok && json.Unmarshal(raw, &fs) == nil {
				out.Files = append(out.Files, fs...)
			}
		}
		var fs []File
		if raw, ok := m["root_files"]; ok && json.Unmarshal(raw, &fs) == nil {
			out.Files = append(out.Files, fs...)
		}
	}
	for key, raw := range m {
		switch key {
		case "all_files", "root_files", "frozen?", "metadata", "cookbook_name", "name", "version", "identifier", "chef_type", "json_class", "uri":
		default:
			if segments[key] {
				continue
			}
			out.Extra[key] = raw
		}
	}
	if raw, ok := m["version"]; ok {
		out.Extra["version"] = raw
	}
	if raw, ok := m["identifier"]; ok {
		out.Extra["identifier"] = raw
	}
	_ = json.Unmarshal(m["frozen?"], &out.Frozen)
	out.Metadata = m["metadata"]
	if len(out.Metadata) == 0 {
		out.Metadata = json.RawMessage(`{}`)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil
}

// Fetch gets and parses a manifest from the server.
func Fetch(ctx context.Context, c *chef.Client, k *kinds.Kind, id kinds.ID) (*Manifest, error) {
	body, err := k.Get(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return Parse(k, id, body)
}

// Body builds the JSON to PUT for the manifest (no URLs).
func (m *Manifest) Body(frozen bool) map[string]any {
	files := make([]File, len(m.Files))
	for i, f := range m.Files {
		f.URL = ""
		files[i] = f
	}
	out := map[string]any{
		"metadata":  m.Metadata,
		"all_files": files,
		"frozen?":   frozen,
		"chef_type": "cookbook_version",
	}
	if m.Kind == kinds.Artifact {
		// Artifacts are named by bare cookbook name; the identifier is separate.
		out["name"] = m.ID.Name
		out["identifier"] = m.ID.Sub
		if v, ok := m.Extra["version"]; ok {
			out["version"] = v
		} else {
			out["version"] = kinds.Field(m.Metadata, "version")
		}
	} else {
		out["name"] = m.ID.Name + "-" + m.ID.Sub
		out["cookbook_name"] = m.ID.Name
		out["version"] = m.ID.Sub
		out["json_class"] = "Chef::CookbookVersion"
	}
	return out
}

// Dependencies returns metadata.dependencies (name -> constraint).
func (m *Manifest) Dependencies() map[string]string {
	var md struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	_ = json.Unmarshal(m.Metadata, &md)
	if md.Dependencies == nil {
		return map[string]string{}
	}
	return md.Dependencies
}

// SafeRelPath validates a manifest path before it becomes a filesystem path:
// relative, slash-separated, no "." or ".." elements, no backslashes.
func SafeRelPath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("unsafe cookbook file path '%s'", p)
	}
	for _, el := range strings.Split(p, "/") {
		if el == "" || el == "." || el == ".." {
			return "", fmt.Errorf("unsafe cookbook file path '%s'", p)
		}
	}
	if len(p) > 1 && p[1] == ':' { // C:foo on Windows
		return "", fmt.Errorf("unsafe cookbook file path '%s'", p)
	}
	return p, nil
}

// Download writes every file of the manifest under dir, verifying checksums,
// and adds a metadata.json if the cookbook does not carry one.
func Download(ctx context.Context, c *chef.Client, m *Manifest, dir string, workers int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	hasMetadataJSON := false
	for _, f := range m.Files {
		if f.Path == "metadata.json" {
			hasMetadataJSON = true
		}
	}
	errs := cli.ForEach(ctx, workers, m.Files, func(f File) string { return f.Path }, func(ctx context.Context, f File) error {
		rel, err := SafeRelPath(f.Path)
		if err != nil {
			return err
		}
		if f.URL == "" {
			return fmt.Errorf("no download URL for %s", f.Path)
		}
		data, err := c.Fetch(ctx, f.URL)
		if err != nil {
			return err
		}
		if sum := Checksum(data); sum != f.Checksum {
			return fmt.Errorf("checksum mismatch for %s: manifest %s, downloaded %s", f.Path, f.Checksum, sum)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if len(errs) > 0 {
		return errs
	}
	if !hasMetadataJSON {
		var pretty json.RawMessage = m.Metadata
		data, err := json.MarshalIndent(json.RawMessage(pretty), "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Checksum is the hex MD5 Chef uses to identify cookbook file content.
func Checksum(data []byte) string {
	sum := md5.Sum(data) // #nosec G401
	return hex.EncodeToString(sum[:])
}

// LoadDir builds a manifest from a cookbook directory. metadata.json must be
// present (metadata.rb is Ruby and is not evaluated).
func LoadDir(k *kinds.Kind, id kinds.ID, dir string) (*Manifest, map[string]string, error) {
	metaPath := filepath.Join(dir, "metadata.json")
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%s: no metadata.json (metadata.rb cannot be evaluated; generate metadata.json with knife or chef first)", dir)
		}
		return nil, nil, err
	}
	if !json.Valid(meta) {
		return nil, nil, fmt.Errorf("%s: not valid JSON", metaPath)
	}
	m := &Manifest{Kind: k, ID: id, Metadata: meta, Extra: map[string]json.RawMessage{}}
	if id.Sub == "" {
		if k == kinds.Cookbook {
			m.ID.Sub = kinds.Field(meta, "version")
		}
		if m.ID.Sub == "" {
			return nil, nil, fmt.Errorf("%s: no %s given and none in metadata.json", dir, k.SubLabel)
		}
	}
	if k == kinds.Artifact {
		if v := kinds.Field(meta, "version"); v != "" {
			m.Extra["version"], _ = json.Marshal(v)
		}
	}
	paths := map[string]string{} // checksum -> absolute path
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && (d.Name() == ".git" || d.Name() == ".svn") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Name() == ".DS_Store" || rel == ".gnife.json" || rel == ".frozen" {
			return nil // gnife's own markers, not cookbook content
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := Checksum(data)
		name, spec := classify(rel)
		m.Files = append(m.Files, File{Name: name, Path: rel, Checksum: sum, Specificity: spec})
		paths[sum] = p
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, paths, nil
}

// classify derives the manifest name and specificity for a relative path the
// way Chef's CookbookManifest does.
func classify(rel string) (name, specificity string) {
	parts := strings.Split(rel, "/")
	if len(parts) == 1 {
		return "root_files/" + rel, "default"
	}
	seg := parts[0]
	name = seg + "/" + parts[len(parts)-1]
	if seg == "files" || seg == "templates" {
		if len(parts) == 2 {
			return name, "root_default"
		}
		return name, parts[1]
	}
	return name, "default"
}

// UploadOptions control Upload.
type UploadOptions struct {
	Force   bool // overwrite a frozen version
	Freeze  bool // freeze after upload
	Workers int
}

type sandbox struct {
	URI       string `json:"uri"`
	SandboxID string `json:"sandbox_id"`
	Checksums map[string]struct {
		URL         string `json:"url"`
		NeedsUpload bool   `json:"needs_upload"`
	} `json:"checksums"`
}

// Upload pushes a manifest and its files: sandbox, per-checksum PUTs for
// what the server lacks, sandbox commit, then the manifest PUT. read
// returns the content for a checksum.
func Upload(ctx context.Context, c *chef.Client, m *Manifest, read func(context.Context, string) ([]byte, error), opts UploadOptions) error {
	sums := map[string]any{}
	for _, f := range m.Files {
		sums[f.Checksum] = nil
	}
	var sb sandbox
	if len(sums) > 0 {
		if err := c.Post(ctx, c.OrgPath("/sandboxes"), map[string]any{"checksums": sums}, &sb); err != nil {
			return fmt.Errorf("creating sandbox: %w", err)
		}
		var todo []string
		for sum, e := range sb.Checksums {
			if e.NeedsUpload {
				todo = append(todo, sum)
			}
		}
		sort.Strings(todo)
		errs := cli.ForEach(ctx, opts.Workers, todo, func(s string) string { return s }, func(ctx context.Context, sum string) error {
			data, err := read(ctx, sum)
			if err != nil {
				return err
			}
			if got := Checksum(data); got != sum {
				return fmt.Errorf("content for checksum %s hashes to %s", sum, got)
			}
			raw, _ := hex.DecodeString(sum)
			return c.Upload(ctx, sb.Checksums[sum].URL, data, base64.StdEncoding.EncodeToString(raw))
		})
		if len(errs) > 0 {
			return errs
		}
		if err := c.Put(ctx, c.OrgPath("/sandboxes/"+url.PathEscape(sb.SandboxID)), map[string]bool{"is_completed": true}, nil); err != nil {
			return fmt.Errorf("committing sandbox: %w", err)
		}
	}
	p := m.Kind.ObjectPath(c, m.ID)
	if opts.Force {
		p += "?force=true"
	}
	if err := c.Put(ctx, p, m.Body(opts.Freeze), nil); err != nil {
		return err
	}
	return nil
}

// UploadDir uploads a cookbook directory.
func UploadDir(ctx context.Context, c *chef.Client, k *kinds.Kind, id kinds.ID, dir string, opts UploadOptions) (*Manifest, error) {
	m, paths, err := LoadDir(k, id, dir)
	if err != nil {
		return nil, err
	}
	err = Upload(ctx, c, m, func(_ context.Context, sum string) ([]byte, error) {
		return os.ReadFile(paths[sum])
	}, opts)
	return m, err
}

// Copy transfers one cookbook or artifact version between servers without
// touching disk; the source's pre-signed URLs are fetched on demand.
func Copy(ctx context.Context, src, dst *chef.Client, k *kinds.Kind, id kinds.ID, opts UploadOptions) error {
	m, err := Fetch(ctx, src, k, id)
	if err != nil {
		return err
	}
	urls := map[string]string{}
	for _, f := range m.Files {
		urls[f.Checksum] = f.URL
	}
	var mu sync.Mutex
	cache := map[string][]byte{}
	read := func(ctx context.Context, sum string) ([]byte, error) {
		mu.Lock()
		data, ok := cache[sum]
		mu.Unlock()
		if ok {
			return data, nil
		}
		u, ok := urls[sum]
		if !ok || u == "" {
			return nil, fmt.Errorf("no source URL for checksum %s", sum)
		}
		data, err := src.Fetch(ctx, u)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		cache[sum] = data
		mu.Unlock()
		return data, nil
	}
	if !opts.Freeze {
		opts.Freeze = m.Frozen
	}
	return Upload(ctx, dst, m, read, opts)
}

// DirName is the knife-ec-backup directory for a version: name-version.
func DirName(id kinds.ID) string { return id.Name + "-" + id.Sub }

// ParseDirName splits name-version back into an ID (the last hyphen wins,
// since cookbook names may contain hyphens and versions never do).
func ParseDirName(dirName string) (kinds.ID, bool) {
	i := strings.LastIndexByte(dirName, '-')
	if i <= 0 || i == len(dirName)-1 {
		return kinds.ID{}, false
	}
	return kinds.ID{Name: dirName[:i], Sub: dirName[i+1:]}, true
}
