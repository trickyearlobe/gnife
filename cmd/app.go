package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/trickyearlobe/gnife/internal/chef"
	"github.com/trickyearlobe/gnife/internal/cli"
	"github.com/trickyearlobe/gnife/internal/config"
	"github.com/trickyearlobe/gnife/internal/credentials"
)

// app carries global flag values and lazily built shared state.
type app struct {
	build BuildInfo

	flagProfile     string
	flagConfig      string
	flagCredentials string
	flagConcurrency int
	flagOutput      string
	flagQuiet       bool
	flagDebug       bool

	mu      sync.Mutex
	cfg     *config.Config
	creds   *credentials.File
	clients map[string]*chef.Client
	used    []string // profiles a client was built for, in order
}

func (a *app) stdout() io.Writer { return os.Stdout }

// progressf writes a progress line to stderr unless --quiet.
func (a *app) progressf(format string, args ...any) {
	if !a.flagQuiet {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

func (a *app) config() (*config.Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg != nil {
		return a.cfg, nil
	}
	path := a.flagConfig
	if path == "" {
		path = config.DefaultPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	a.cfg = c
	return c, nil
}

func (a *app) credentialsFile() (*credentials.File, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.creds != nil {
		return a.creds, nil
	}
	path := a.flagCredentials
	if path == "" {
		if c, err := a.configLocked(); err == nil && c.Get("credentials") != "" {
			path = c.Get("credentials")
		}
	}
	if path == "" {
		path = credentials.DefaultPath()
	}
	f, err := credentials.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}
	a.creds = f
	return f, nil
}

// configLocked is config() for callers already holding a.mu.
func (a *app) configLocked() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	path := a.flagConfig
	if path == "" {
		path = config.DefaultPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	a.cfg = c
	return c, nil
}

// profileName resolves the default profile: flag, $CHEF_PROFILE, config,
// ~/.chef/context, "default".
func (a *app) profileName() string {
	if a.flagProfile != "" {
		return a.flagProfile
	}
	if v := os.Getenv("CHEF_PROFILE"); v != "" {
		return v
	}
	if c, err := a.config(); err == nil && c.Get("profile") != "" {
		return c.Get("profile")
	}
	if v := credentials.ContextProfile(); v != "" {
		return v
	}
	return "default"
}

// profile returns the named profile with environment overrides applied.
func (a *app) profile(name string) (*credentials.Profile, error) {
	f, err := a.credentialsFile()
	if err != nil {
		return nil, err
	}
	p, ok := f.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile '%s' not found in %s (have: %v)", name, f.Path, f.Order)
	}
	cp := *p
	cp.ApplyEnv()
	return &cp, nil
}

// client returns a cached client for the named profile.
func (a *app) client(name string) (*chef.Client, error) {
	a.mu.Lock()
	if c, ok := a.clients[name]; ok {
		a.mu.Unlock()
		return c, nil
	}
	a.mu.Unlock()

	p, err := a.profile(name)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	key, err := p.PrivateKeyPEM()
	if err != nil {
		return nil, err
	}
	cfg := chef.Config{
		ServerURL:          p.ServerURL,
		ClientName:         p.ClientName,
		PrivateKeyPEM:      key,
		InsecureSkipVerify: p.InsecureSkipVerify(),
		TrustedCerts:       credentials.TrustedCerts(),
		UserAgent:          "gnife/" + a.build.Version,
	}
	if a.flagDebug {
		cfg.Trace = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "debug: ["+name+"] "+format+"\n", args...)
		}
		if cfg.InsecureSkipVerify {
			cfg.Trace("ssl_verify_mode :verify_none; TLS certificate verification is disabled")
		}
	}
	c, err := chef.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("profile '%s': %w", name, err)
	}
	a.mu.Lock()
	if a.clients == nil {
		a.clients = map[string]*chef.Client{}
	}
	a.clients[name] = c
	a.used = append(a.used, name+" ("+p.ClientName+")")
	a.mu.Unlock()
	return c, nil
}

// defaultClient is the client for --profile (or its fallbacks).
func (a *app) defaultClient() (*chef.Client, error) {
	return a.client(a.profileName())
}

// orgClient is defaultClient but insists the profile names an organisation.
func (a *app) orgClient() (*chef.Client, error) {
	c, err := a.defaultClient()
	if err != nil {
		return nil, err
	}
	if c.Org() == "" {
		return nil, fmt.Errorf("profile '%s' has no organisation in its chef_server_url", a.profileName())
	}
	return c, nil
}

// sourceDest resolves --from/--to, falling back to config source/dest.
func (a *app) sourceDest(from, to string) (src, dst *chef.Client, err error) {
	c, err := a.config()
	if err != nil {
		return nil, nil, err
	}
	if from == "" {
		from = c.Get("source")
	}
	if to == "" {
		to = c.Get("dest")
	}
	if from == "" || to == "" {
		return nil, nil, usageErr("--from and --to are required (or set them with: gnife config set source|dest PROFILE)")
	}
	if from == to {
		return nil, nil, usageErr("--from and --to are both '%s'", from)
	}
	if src, err = a.client(from); err != nil {
		return nil, nil, err
	}
	if dst, err = a.client(to); err != nil {
		return nil, nil, err
	}
	if src.Org() == "" || dst.Org() == "" {
		return nil, nil, fmt.Errorf("both --from and --to profiles must name an organisation")
	}
	if src.ServerRoot() == dst.ServerRoot() && src.Org() == dst.Org() {
		return nil, nil, usageErr("--from and --to point at the same organisation")
	}
	return src, dst, nil
}

func (a *app) concurrency() int {
	if a.flagConcurrency > 0 {
		return a.flagConcurrency
	}
	if c, err := a.config(); err == nil {
		if n, err := strconv.Atoi(c.Get("concurrency")); err == nil && n > 0 {
			return n
		}
	}
	return 16
}

func (a *app) output() string {
	if a.flagOutput != "" {
		return a.flagOutput
	}
	if c, err := a.config(); err == nil && c.Get("output") != "" {
		return c.Get("output")
	}
	return "json"
}

// printList prints names as JSON or one per line depending on --output.
func (a *app) printList(names []string) error {
	switch a.output() {
	case "names":
		cli.PrintNames(a.stdout(), names)
		return nil
	case "json":
		if names == nil {
			names = []string{}
		}
		return cli.PrintJSON(a.stdout(), names)
	default:
		return usageErr("unknown output format '%s' (json|names)", a.output())
	}
}
