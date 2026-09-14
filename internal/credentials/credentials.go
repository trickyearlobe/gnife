// Package credentials reads the standard Chef ~/.chef/credentials file.
//
// The file is TOML, but Chef only ever writes and documents a small subset:
// [profile] tables, [profile.knife] sub-tables, basic/literal/multi-line
// strings and booleans. That subset is parsed here directly rather than
// pulling in a TOML library.
package credentials

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Profile is one [section] of the credentials file.
type Profile struct {
	Name                 string
	ServerURL            string // chef_server_url
	ClientName           string // client_name (alias node_name)
	ClientKey            string // path or inline PEM
	SSLVerifyMode        string // ":verify_peer" | ":verify_none"
	ValidationClientName string
	ValidationKey        string
	// Extra holds any other top-level keys verbatim.
	Extra map[string]string

	dir string // directory of the credentials file, for relative key paths
}

// File is a parsed credentials file.
type File struct {
	Path     string
	Profiles map[string]*Profile
	Order    []string // profile names in file order
}

// DefaultPath is ~/.chef/credentials, or $CHEF_CREDENTIALS_FILE if set.
func DefaultPath() string {
	if p := os.Getenv("CHEF_CREDENTIALS_FILE"); p != "" {
		return p
	}
	return filepath.Join(ChefDir(), "credentials")
}

// ChefDir is ~/.chef.
func ChefDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".chef")
}

// Load parses the credentials file at path.
func Load(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	file, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	file.Path = path
	dir := filepath.Dir(path)
	for _, p := range file.Profiles {
		p.dir = dir
	}
	return file, nil
}

// Parse parses credentials-file content.
func Parse(r io.Reader) (*File, error) {
	file := &File{Profiles: map[string]*Profile{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var (
		cur     *Profile
		subtab  bool // inside [profile.knife] etc: parsed, ignored
		lineNo  int
		lines   []string
		pending string
	)
	// Read all lines so multi-line strings can span them.
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for i := 0; i < len(lines); i++ {
		lineNo = i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated table header", lineNo)
			}
			name := strings.TrimSpace(line[1:end])
			name = strings.Trim(name, `"'`)
			if dot := strings.IndexByte(name, '.'); dot >= 0 {
				parent := strings.Trim(name[:dot], `"' `)
				cur = file.profile(parent)
				subtab = true
				continue
			}
			cur = file.profile(name)
			subtab = false
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: key outside of a [profile] table", lineNo)
		}
		key := strings.Trim(strings.TrimSpace(line[:eq]), `"'`)
		raw := strings.TrimSpace(line[eq+1:])

		var value string
		switch {
		case strings.HasPrefix(raw, `"""`):
			// Multi-line basic string; may close on the same line.
			body := raw[3:]
			if idx := strings.Index(body, `"""`); idx >= 0 {
				value = body[:idx]
			} else {
				pending = strings.TrimPrefix(body, "\n")
				closed := false
				for i++; i < len(lines); i++ {
					l := lines[i]
					if idx := strings.Index(l, `"""`); idx >= 0 {
						pending += l[:idx]
						closed = true
						break
					}
					pending += l + "\n"
				}
				if !closed {
					return nil, fmt.Errorf("line %d: unterminated multi-line string", lineNo)
				}
				// TOML trims the newline right after the opening delimiter.
				value = strings.TrimPrefix(pending, "\n")
			}
			value = unescape(value)
		case strings.HasPrefix(raw, `'''`):
			body := raw[3:]
			if idx := strings.Index(body, `'''`); idx >= 0 {
				value = body[:idx]
			} else {
				pending = body
				closed := false
				for i++; i < len(lines); i++ {
					l := lines[i]
					if idx := strings.Index(l, `'''`); idx >= 0 {
						pending += l[:idx]
						closed = true
						break
					}
					pending += l + "\n"
				}
				if !closed {
					return nil, fmt.Errorf("line %d: unterminated multi-line literal string", lineNo)
				}
				value = strings.TrimPrefix(pending, "\n")
			}
		case strings.HasPrefix(raw, `"`):
			s, err := basicString(raw)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			value = s
		case strings.HasPrefix(raw, `'`):
			end := strings.IndexByte(raw[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated literal string", lineNo)
			}
			value = raw[1 : 1+end]
		default:
			// bare value: bool, number, or a bare word (be lenient; strip a comment)
			if c := strings.IndexByte(raw, '#'); c >= 0 {
				raw = strings.TrimSpace(raw[:c])
			}
			value = raw
		}
		if subtab {
			continue
		}
		cur.set(key, value)
	}
	return file, nil
}

func (f *File) profile(name string) *Profile {
	if p, ok := f.Profiles[name]; ok {
		return p
	}
	p := &Profile{Name: name, Extra: map[string]string{}}
	f.Profiles[name] = p
	f.Order = append(f.Order, name)
	return p
}

func (p *Profile) set(key, value string) {
	switch key {
	case "chef_server_url":
		p.ServerURL = value
	case "client_name", "node_name":
		p.ClientName = value
	case "client_key":
		p.ClientKey = value
	case "ssl_verify_mode":
		p.SSLVerifyMode = value
	case "validation_client_name":
		p.ValidationClientName = value
	case "validation_key":
		p.ValidationKey = value
	default:
		p.Extra[key] = value
	}
}

// basicString parses a one-line "..." string with TOML escapes, ignoring a
// trailing comment.
func basicString(raw string) (string, error) {
	var sb strings.Builder
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case '"':
			return sb.String(), nil
		case '\\':
			if i+1 >= len(raw) {
				return "", errors.New("dangling backslash in string")
			}
			i++
			switch raw[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case 'u', 'U':
				n := 4
				if raw[i] == 'U' {
					n = 8
				}
				if i+n >= len(raw) {
					return "", errors.New("truncated unicode escape")
				}
				cp, err := strconv.ParseUint(raw[i+1:i+1+n], 16, 32)
				if err != nil {
					return "", fmt.Errorf("bad unicode escape: %w", err)
				}
				sb.WriteRune(rune(cp))
				i += n
			default:
				return "", fmt.Errorf("unknown escape \\%c", raw[i])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return "", errors.New("unterminated string")
}

// unescape applies the basic-string escapes to a multi-line body. Chef writes
// PEM keys into these, which contain no escapes, so this is rarely exercised.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	out, err := basicString(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`)
	if err != nil {
		return s
	}
	return out
}

// Org returns the organisation name from the server URL, or "".
func (p *Profile) Org() string {
	u, err := url.Parse(p.ServerURL)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	if i := strings.Index(path, "/organizations/"); i >= 0 {
		rest := path[i+len("/organizations/"):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	return ""
}

// PrivateKeyPEM returns the private key bytes: the inline PEM if client_key
// is one, otherwise the contents of the file it names (relative paths and ~
// resolve against the credentials directory and home respectively).
func (p *Profile) PrivateKeyPEM() ([]byte, error) {
	if p.ClientKey == "" {
		return nil, fmt.Errorf("profile '%s' has no client_key", p.Name)
	}
	if strings.Contains(p.ClientKey, "-----BEGIN") {
		return []byte(p.ClientKey), nil
	}
	path := p.KeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile '%s': reading client_key: %w", p.Name, err)
	}
	return data, nil
}

// KeyPath returns the resolved path of client_key ("" when inline).
func (p *Profile) KeyPath() string {
	if strings.Contains(p.ClientKey, "-----BEGIN") {
		return ""
	}
	path := p.ClientKey
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		dir := p.dir
		if dir == "" {
			dir = ChefDir()
		}
		path = filepath.Join(dir, path)
	}
	return path
}

// InsecureSkipVerify reports whether ssl_verify_mode disables verification.
func (p *Profile) InsecureSkipVerify() bool {
	return strings.TrimPrefix(p.SSLVerifyMode, ":") == "verify_none"
}

// ApplyEnv overlays knife's environment overrides onto the profile.
func (p *Profile) ApplyEnv() {
	if v := os.Getenv("CHEF_SERVER_URL"); v != "" {
		p.ServerURL = v
	}
	if v := os.Getenv("CHEF_NODE_NAME"); v != "" {
		p.ClientName = v
	}
	if v := os.Getenv("CHEF_CLIENT_KEY"); v != "" {
		p.ClientKey = v
	}
}

// Validate checks the fields a client needs.
func (p *Profile) Validate() error {
	var missing []string
	if p.ServerURL == "" {
		missing = append(missing, "chef_server_url")
	}
	if p.ClientName == "" {
		missing = append(missing, "client_name")
	}
	if p.ClientKey == "" {
		missing = append(missing, "client_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("profile '%s' is missing %s", p.Name, strings.Join(missing, ", "))
	}
	return nil
}

// ContextProfile returns the profile named in ~/.chef/context, or "".
func ContextProfile() string {
	data, err := os.ReadFile(filepath.Join(ChefDir(), "context"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TrustedCerts concatenates every PEM file in ~/.chef/trusted_certs.
func TrustedCerts() []byte {
	entries, err := os.ReadDir(filepath.Join(ChefDir(), "trusted_certs"))
	if err != nil {
		return nil
	}
	var out []byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ChefDir(), "trusted_certs", e.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "-----BEGIN CERTIFICATE-----") {
			out = append(out, data...)
			out = append(out, '\n')
		}
	}
	return out
}
