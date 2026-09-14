// Package backup writes and reads organisations in the knife-ec-backup
// directory layout, and restores them through the transfer engine.
package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trickyearlobe/gnife/internal/cookbook"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// Marker is the gnife sidecar written into each organisation directory.
const Marker = ".gnife.json"

// MarkerInfo is the content of the sidecar.
type MarkerInfo struct {
	Tool       string `json:"tool"`
	Version    string `json:"version"`
	ServerURL  string `json:"server_url"`
	Org        string `json:"organization"`
	APIVersion string `json:"api_version"`
	Created    string `json:"created"`
}

// JSONKinds are the object types stored as one JSON file each, in restore order.
var JSONKinds = []*kinds.Kind{
	kinds.Container, kinds.Group, kinds.Client, kinds.Environment, kinds.Role,
	kinds.DataBagItem, kinds.Node, kinds.Policy, kinds.PolicyGroup,
}

// FilesKinds are stored as directories.
var FilesKinds = []*kinds.Kind{kinds.Cookbook, kinds.Artifact}

// OrgDir is DIR/organizations/NAME.
func OrgDir(root, org string) string { return filepath.Join(root, "organizations", org) }

// SafeName rejects object names that cannot be file names.
func SafeName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("object name '%s' cannot be used as a file name", name)
	}
	return nil
}

// ObjectPath is where one object lives under the organisation directory.
func ObjectPath(orgDir string, k *kinds.Kind, id kinds.ID) (string, error) {
	if err := SafeName(id.Name); err != nil {
		return "", err
	}
	if k.Composite {
		if err := SafeName(id.Sub); err != nil {
			return "", err
		}
	}
	switch k {
	case kinds.Cookbook, kinds.Artifact:
		return filepath.Join(orgDir, k.Plural, cookbook.DirName(id)), nil
	case kinds.DataBagItem:
		return filepath.Join(orgDir, "data_bags", id.Name, id.Sub+".json"), nil
	case kinds.DataBag:
		return filepath.Join(orgDir, "data_bags", id.Name), nil
	case kinds.Policy:
		return filepath.Join(orgDir, "policies", id.Name+"-"+id.Sub+".json"), nil
	}
	return filepath.Join(orgDir, k.Plural, id.Name+".json"), nil
}

// ACLPath is acls/<plural>/<name>.json, or acls/organization.json for k == nil.
func ACLPath(orgDir string, k *kinds.Kind, name string) (string, error) {
	if k == nil {
		return filepath.Join(orgDir, "acls", "organization.json"), nil
	}
	if err := SafeName(name); err != nil {
		return "", err
	}
	return filepath.Join(orgDir, "acls", k.Plural, name+".json"), nil
}

// WriteJSON writes v as indented JSON, creating parent directories.
func WriteJSON(path string, v any) error {
	var data []byte
	var err error
	switch b := v.(type) {
	case json.RawMessage:
		var buf any
		if err = json.Unmarshal(b, &buf); err != nil {
			return err
		}
		data, err = json.MarshalIndent(buf, "", "  ")
	case []byte:
		return WriteJSON(path, json.RawMessage(b))
	default:
		data, err = json.MarshalIndent(v, "", "  ")
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func readJSON(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("%s: not valid JSON", path)
	}
	return data, nil
}

// listJSONNames returns the names of *.json files in dir (without extension).
func listJSONNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	return names, nil
}

// listDirs returns the subdirectory names of dir.
func listDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
