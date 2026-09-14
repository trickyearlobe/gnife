package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadSetSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	c, err := Load(path) // missing file is fine
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set("source", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("concurrency", "x"); err == nil {
		t.Fatal("expected validation error")
	}
	if err := c.Set("output", "yaml"); err == nil {
		t.Fatal("expected validation error")
	}
	if err := c.Set("nope", "1"); err == nil {
		t.Fatal("expected unknown key error")
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
	c2, err := Load(path)
	if err != nil || c2.Get("source") != "prod" {
		t.Fatalf("reload: %v %q", err, c2.Get("source"))
	}
	if err := c2.Unset("source"); err != nil {
		t.Fatal(err)
	}
	if c2.Get("source") != "" {
		t.Fatal("unset")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "bad.json")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := Load(bad); err == nil {
		t.Fatal("expected parse error")
	}
}
