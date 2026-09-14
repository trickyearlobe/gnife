// Package config stores gnife's own defaults in ~/.gnife/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Keys are the settings gnife understands, with their help text.
var Keys = map[string]string{
	"profile":     "default --profile",
	"source":      "default --from for copy commands",
	"dest":        "default --to for copy commands",
	"concurrency": "worker count for bulk operations (default 16)",
	"credentials": "path to the Chef credentials file (default ~/.chef/credentials)",
	"output":      "default output format: json or names",
}

// Config is a loaded config file.
type Config struct {
	Path   string
	Values map[string]string
}

// DefaultPath is $GNIFE_CONFIG or ~/.gnife/config.json.
func DefaultPath() string {
	if p := os.Getenv("GNIFE_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".gnife", "config.json")
}

// Load reads the file at path; a missing file yields an empty config.
func Load(path string) (*Config, error) {
	c := &Config{Path: path, Values: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c.Values); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Get returns a value or "".
func (c *Config) Get(key string) string { return c.Values[key] }

// Set validates key and value and stores them (Save persists).
func (c *Config) Set(key, value string) error {
	if _, ok := Keys[key]; !ok {
		return fmt.Errorf("unknown config key '%s' (known: %s)", key, KeyList())
	}
	switch key {
	case "concurrency":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("concurrency must be a positive integer, got '%s'", value)
		}
	case "output":
		if value != "json" && value != "names" {
			return fmt.Errorf("output must be json or names, got '%s'", value)
		}
	}
	c.Values[key] = value
	return nil
}

// Unset removes a key.
func (c *Config) Unset(key string) error {
	if _, ok := Keys[key]; !ok {
		return fmt.Errorf("unknown config key '%s' (known: %s)", key, KeyList())
	}
	delete(c.Values, key)
	return nil
}

// Save writes the file atomically with mode 0600.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.Values, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.Path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, c.Path)
}

// KeyList returns the known keys, sorted, comma separated.
func KeyList() string {
	keys := make([]string, 0, len(Keys))
	for k := range Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += ", "
		}
		s += k
	}
	return s
}
