package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# knife credentials
[default]
client_name = "richard"
client_key = "richard.pem"
chef_server_url = "https://chef.example.com/organizations/thenixons"
ssl_verify_mode = ":verify_none"

[prod]
node_name = 'ops'
client_key = """
-----BEGIN RSA PRIVATE KEY-----
MIIabc
-----END RSA PRIVATE KEY-----
"""
chef_server_url = "https://prod.example.com/organizations/prod" # trailing comment
validation_client_name = "prod-validator"

[prod.knife]
ssh_user = "ubuntu"
editor = "vim"

["quoted name"]
client_name = "a\"b\\c\n"
client_key = "/abs/key.pem"
chef_server_url = "https://x"
`

func TestParse(t *testing.T) {
	f, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.Order, ","); got != "default,prod,quoted name" {
		t.Fatalf("order: %s", got)
	}
	d := f.Profiles["default"]
	if d.ClientName != "richard" || d.ClientKey != "richard.pem" || d.Org() != "thenixons" || !d.InsecureSkipVerify() {
		t.Fatalf("default: %+v", d)
	}
	p := f.Profiles["prod"]
	if p.ClientName != "ops" {
		t.Fatalf("node_name alias: %q", p.ClientName)
	}
	if !strings.HasPrefix(p.ClientKey, "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END") {
		t.Fatalf("multi-line key: %q", p.ClientKey)
	}
	if p.ServerURL != "https://prod.example.com/organizations/prod" {
		t.Fatalf("url with comment: %q", p.ServerURL)
	}
	if p.ValidationClientName != "prod-validator" {
		t.Fatalf("validation_client_name: %q", p.ValidationClientName)
	}
	if _, ok := p.Extra["ssh_user"]; ok {
		t.Fatal("knife sub-table keys must be ignored")
	}
	q := f.Profiles["quoted name"]
	if q.ClientName != "a\"b\\c\n" {
		t.Fatalf("escapes: %q", q.ClientName)
	}
	pem, err := p.PrivateKeyPEM()
	if err != nil || !strings.Contains(string(pem), "MIIabc") {
		t.Fatalf("inline key: %v %q", err, pem)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		"client_name = \"x\"\n",    // key outside table
		"[a]\nclient_name\n",       // no =
		"[a]\nclient_name = \"x\n", // unterminated
		"[a\n",                     // unterminated header
		"[a]\nk = \"\"\"\nabc\n",   // unterminated multi-line
	} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestLoadResolvesRelativeKeyPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "k.pem"), []byte("-----BEGIN RSA PRIVATE KEY-----\nzz\n-----END RSA PRIVATE KEY-----\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "credentials"), []byte("[default]\nclient_name = \"c\"\nclient_key = \"k.pem\"\nchef_server_url = \"https://h/organizations/o\"\n"), 0o600)
	f, err := Load(filepath.Join(dir, "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Profiles["default"]
	if p.KeyPath() != filepath.Join(dir, "k.pem") {
		t.Fatalf("key path: %s", p.KeyPath())
	}
	if pem, err := p.PrivateKeyPEM(); err != nil || !strings.Contains(string(pem), "zz") {
		t.Fatalf("read key: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&Profile{Name: "x"}).Validate(); err == nil || !strings.Contains(err.Error(), "chef_server_url") {
		t.Fatalf("validate: %v", err)
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("CHEF_SERVER_URL", "https://env/organizations/e")
	t.Setenv("CHEF_NODE_NAME", "envuser")
	p := &Profile{ServerURL: "https://file", ClientName: "fileuser"}
	p.ApplyEnv()
	if p.ServerURL != "https://env/organizations/e" || p.ClientName != "envuser" {
		t.Fatalf("env override: %+v", p)
	}
}
