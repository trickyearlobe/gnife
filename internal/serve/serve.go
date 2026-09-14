package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/trickyearlobe/gnife/internal/chefserver"
	"github.com/trickyearlobe/gnife/internal/kinds"
)

// Options configure Run.
type Options struct {
	Dir      string // backup-layout directory; created if missing
	Listen   string // host:port
	TLS      bool   // serve HTTPS with a generated certificate unless CertFile/KeyFile are set
	CertFile string
	KeyFile  string
	Org      string // organisation to create if absent
	Version  string
	Logf     func(format string, args ...any) // request log
	Infof    func(format string, args ...any) // startup messages
}

// Run serves until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Infof == nil {
		opts.Infof = func(string, ...any) {}
	}
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:8889"
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return err
	}
	store := &DirStore{Root: opts.Dir}
	s := chefserver.NewServer(store)
	s.Version = opts.Version
	s.Logf = opts.Logf
	if err := store.Load(s); err != nil {
		return fmt.Errorf("loading %s: %w", opts.Dir, err)
	}

	// The superuser: reuse a backed-up pivotal (its public key came with
	// the backup, so the matching pivotal.pem keeps working), else make one.
	pivotalKey := filepath.Join(opts.Dir, "pivotal.pem")
	if !s.HasUser("pivotal") {
		priv, err := s.GeneratePivotal()
		if err != nil {
			return err
		}
		if err := os.WriteFile(pivotalKey, priv, 0o600); err != nil {
			return err
		}
		opts.Infof("created superuser pivotal; key written to %s", pivotalKey)
	} else if _, err := os.Stat(pivotalKey); err == nil {
		opts.Infof("superuser pivotal loaded; key at %s", pivotalKey)
	} else {
		opts.Infof("superuser pivotal loaded from the backup; use the pivotal.pem of the server it came from")
	}

	if opts.Org != "" {
		if _, ok := s.Orgs[opts.Org]; !ok {
			if err := createOrg(s, store, opts); err != nil {
				return err
			}
		}
	}
	for name, o := range s.Orgs {
		opts.Infof("organisation %s: %d nodes, %d roles, %d cookbooks, %d clients", name, len(o.Nodes), len(o.Roles), len(o.Cookbooks), len(o.Clients))
	}

	ln, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second}
	scheme := "http"
	if opts.TLS || opts.CertFile != "" {
		cert, err := loadOrMakeCert(opts)
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		ln = tls.NewListener(ln, srv.TLSConfig)
		scheme = "https"
	}
	opts.Infof("listening on %s://%s  (chef_server_url = \"%s://%s/organizations/ORG\")", scheme, ln.Addr(), scheme, ln.Addr())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// createOrg makes the organisation directly in the server (no HTTP round
// trip) and saves its validator key beside the pivotal key.
func createOrg(s *chefserver.Server, store *DirStore, opts Options) error {
	s.SetOrg(opts.Org, opts.Org, nil)
	if err := store.PutOrg(opts.Org, opts.Org, nil); err != nil {
		return err
	}
	priv, pub, err := keyPair()
	if err != nil {
		return err
	}
	vname := opts.Org + "-validator"
	body, _ := json.Marshal(map[string]any{"name": vname, "validator": true, "public_key": pub})
	if err := s.SetObject(opts.Org, kinds.Client, kinds.ID{Name: vname}, body); err != nil {
		return err
	}
	if err := store.PutObject(opts.Org, kinds.Client, kinds.ID{Name: vname}, body); err != nil {
		return err
	}
	keyFile := filepath.Join(opts.Dir, vname+".pem")
	if err := os.WriteFile(keyFile, priv, 0o600); err != nil {
		return err
	}
	opts.Infof("created organisation %s; validator key written to %s", opts.Org, keyFile)
	return nil
}

func keyPair() ([]byte, string, error) {
	return chefserver.KeyPair()
}

// loadOrMakeCert uses the given certificate or generates a self-signed one
// (persisted in the directory so its fingerprint is stable across restarts).
func loadOrMakeCert(opts Options) (tls.Certificate, error) {
	certFile, keyFile := opts.CertFile, opts.KeyFile
	if certFile == "" {
		certFile = filepath.Join(opts.Dir, "tls.crt")
		keyFile = filepath.Join(opts.Dir, "tls.key")
		if _, err := os.Stat(certFile); err != nil {
			if err := makeSelfSigned(certFile, keyFile, opts.Listen); err != nil {
				return tls.Certificate{}, err
			}
			opts.Infof("generated self-signed certificate %s (add it to ~/.chef/trusted_certs or use ssl_verify_mode :verify_none)", certFile)
		}
	}
	return tls.LoadX509KeyPair(certFile, keyFile)
}

func makeSelfSigned(certFile, keyFile, listen string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(listen)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "gnife serve"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	if hn, err := os.Hostname(); err == nil {
		tmpl.DNSNames = append(tmpl.DNSNames, hn)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}
