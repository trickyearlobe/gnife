// Package chef is a native Go client for the Chef Infra Server API. It signs
// requests with authentication protocol 1.3 (RSA-SHA256) using only the
// standard library.
package chef

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion is the X-Ops-Server-API-Version sent with every request. Version 2
// gives cookbooks the flat all_files manifest; it needs Chef Infra Server >= 12.17.
const APIVersion = "2"

// chefVersion is advertised in X-Chef-Version; the server uses it only for
// compatibility gating of very old clients.
const chefVersion = "18.0.0"

// Config holds the parameters needed to construct a Client.
type Config struct {
	// ServerURL is the server URL, normally including the organisation
	// path: https://chef.example.com/organizations/myorg. A URL without an
	// organisation is valid for server-level (pivotal) work only.
	ServerURL string
	// ClientName is sent in X-Ops-UserId.
	ClientName string
	// PrivateKeyPEM is the RSA private key (PKCS#1 or PKCS#8). The slice is
	// zeroed by NewClient once the key has been parsed.
	PrivateKeyPEM []byte
	// InsecureSkipVerify disables TLS certificate verification.
	InsecureSkipVerify bool
	// TrustedCerts are extra PEM certificates to trust (knife's
	// ~/.chef/trusted_certs). Ignored when InsecureSkipVerify is set.
	TrustedCerts []byte
	// UserAgent defaults to "gnife".
	UserAgent string
	// Retries is the number of attempts for retryable failures (429, 502,
	// 503, 504, connection errors). Default 3; 1 disables retrying.
	Retries int
	// HTTPClient overrides the default transport (tests).
	HTTPClient *http.Client
	// Trace, if set, receives one line per request: method, path, status, duration.
	Trace func(format string, args ...any)
}

// Client is a Chef Infra Server API client bound to one server and,
// optionally, one organisation.
type Client struct {
	base       *url.URL // scheme://host[:port][/prefix], no organisation
	org        string
	clientName string
	privateKey *rsa.PrivateKey
	userAgent  string
	retries    int
	httpClient *http.Client
	trace      func(string, ...any)
}

// NewClient parses the key and URL and returns a ready client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.ServerURL == "" {
		return nil, errors.New("chef: server URL is required")
	}
	if cfg.ClientName == "" {
		return nil, errors.New("chef: client name is required")
	}
	if len(cfg.PrivateKeyPEM) == 0 {
		return nil, errors.New("chef: private key is required")
	}
	base, org, err := SplitServerURL(cfg.ServerURL)
	if err != nil {
		return nil, err
	}
	key, err := ParsePrivateKey(cfg.PrivateKeyPEM)
	zero(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("chef: %w", err)
	}

	ua := cfg.UserAgent
	if ua == "" {
		ua = "gnife"
	}
	retries := cfg.Retries
	if retries <= 0 {
		retries = 3
	}

	hc := cfg.HTTPClient
	if hc == nil {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.InsecureSkipVerify {
			tlsCfg.InsecureSkipVerify = true // #nosec G402 -- user asked for :verify_none
		} else if len(cfg.TrustedCerts) > 0 {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(cfg.TrustedCerts) {
				return nil, errors.New("chef: no certificates found in trusted certs")
			}
			tlsCfg.RootCAs = pool
		}
		hc = newHTTPClient(tlsCfg)
	}

	return &Client{
		base:       base,
		org:        org,
		clientName: cfg.ClientName,
		privateKey: key,
		userAgent:  ua,
		retries:    retries,
		httpClient: hc,
		trace:      cfg.Trace,
	}, nil
}

// SplitServerURL separates https://host/organizations/ORG into the server
// root and the organisation name. The organisation may be absent.
func SplitServerURL(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("chef: invalid server URL '%s': %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, "", fmt.Errorf("chef: invalid server URL '%s': need http(s)://host", raw)
	}
	path := strings.TrimRight(u.Path, "/")
	org := ""
	if i := strings.Index(path, "/organizations/"); i >= 0 {
		rest := path[i+len("/organizations/"):]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		org = rest
		path = path[:i]
	}
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, org, nil
}

// Org returns the organisation the client is bound to ("" for server-level).
func (c *Client) Org() string { return c.org }

// ClientName returns the identity used to sign requests.
func (c *Client) ClientName() string { return c.clientName }

// ServerRoot returns scheme://host[:port][/prefix].
func (c *Client) ServerRoot() string { return c.base.String() }

// OrgPath prefixes p with /organizations/ORG.
func (c *Client) OrgPath(p string) string {
	if c.org == "" {
		panic("chef: OrgPath called on a client with no organisation")
	}
	return "/organizations/" + url.PathEscape(c.org) + p
}

// WithOrg returns a client for another organisation on the same server,
// sharing the transport and key.
func (c *Client) WithOrg(org string) *Client {
	cp := *c
	cp.org = org
	return &cp
}

// Timeouts for the default transport. ResponseHeaderTimeout bounds the wait
// for the server to start replying without capping large transfers.
const (
	httpDialTimeout           = 15 * time.Second
	httpTLSHandshakeTimeout   = 15 * time.Second
	httpResponseHeaderTimeout = 60 * time.Second
	httpIdleConnTimeout       = 90 * time.Second
	httpOverallTimeout        = 15 * time.Minute
	httpMaxIdleConnsPerHost   = 32
)

func newHTTPClient(tlsCfg *tls.Config) *http.Client {
	return &http.Client{
		Timeout: httpOverallTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
			DialContext: (&net.Dialer{
				Timeout:   httpDialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   httpTLSHandshakeTimeout,
			ResponseHeaderTimeout: httpResponseHeaderTimeout,
			IdleConnTimeout:       httpIdleConnTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   httpMaxIdleConnsPerHost,
			ForceAttemptHTTP2:     true,
		},
	}
}

// ParsePrivateKey decodes a PEM-encoded RSA private key in PKCS#1 or PKCS#8 form.
func ParsePrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no PEM block found in private key")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PKCS#8 private key: %w", err)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, not RSA", parsed)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block '%s' (expected RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// sign adds the protocol 1.3 authentication headers to req.
func (c *Client) sign(req *http.Request, body []byte) error {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	bodyHash := sha256.Sum256(body)
	contentHash := base64.StdEncoding.EncodeToString(bodyHash[:])

	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		"Method:" + req.Method,
		"Path:" + path,
		"X-Ops-Content-Hash:" + contentHash,
		"X-Ops-Sign:version=1.3",
		"X-Ops-Timestamp:" + timestamp,
		"X-Ops-UserId:" + c.clientName,
		"X-Ops-Server-API-Version:" + APIVersion,
	}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	sig, err := rsa.SignPKCS1v15(nil, c.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return fmt.Errorf("chef: signing request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-Chef-Version", chefVersion)
	req.Header.Set("X-Ops-Sign", "version=1.3")
	req.Header.Set("X-Ops-Timestamp", timestamp)
	req.Header.Set("X-Ops-UserId", c.clientName)
	req.Header.Set("X-Ops-Content-Hash", contentHash)
	req.Header.Set("X-Ops-Server-API-Version", APIVersion)
	for i, seg := range splitString(base64.StdEncoding.EncodeToString(sig), 60) {
		req.Header.Set(fmt.Sprintf("X-Ops-Authorization-%d", i+1), seg)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return nil
}

func splitString(s string, n int) []string {
	chunks := make([]string, 0, (len(s)+n-1)/n)
	for len(s) > 0 {
		end := min(n, len(s))
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}

// Response is the outcome of a request that reached the server.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   []byte
}

func (e *APIError) Error() string {
	msg := serverMessage(e.Body)
	if msg == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, msg)
}

// serverMessage extracts the "error" field the Chef server puts in JSON error
// bodies, so messages read as `HTTP 404: node 'x' not found` rather than raw
// JSON. The field may be a string, a list of strings, or (the depsolver) a
// list holding an object with a "message".
func serverMessage(body []byte) string {
	var v struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &v); err == nil && v.Error != nil {
		if list, ok := v.Error.([]any); ok {
			parts := make([]string, 0, len(list))
			for _, p := range list {
				parts = append(parts, stringify(p))
			}
			return strings.Join(parts, "; ")
		}
		return stringify(v.Error)
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func stringify(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		if m, ok := e["message"].(string); ok && m != "" {
			return m
		}
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprint(v)
}

func (e *APIError) IsNotFound() bool  { return e.Status == http.StatusNotFound }
func (e *APIError) IsConflict() bool  { return e.Status == http.StatusConflict }
func (e *APIError) IsForbidden() bool { return e.Status == http.StatusForbidden }
func (e *APIError) IsRetryable() bool {
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsNotFound()
}

// IsConflict reports whether err is a 409 from the server.
func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsConflict()
}

// IsForbidden reports whether err is a 403 from the server.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsForbidden()
}

// IsNotPermitted reports whether err is a 401 or 403: the caller's identity
// cannot do this (clients get 401 on server-level endpoints, users 403).
func IsNotPermitted(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusUnauthorized)
}

// Do signs and executes a request. path is relative to the server root and
// may carry a query string. Non-2xx responses are returned as *APIError
// alongside the response. Retryable failures are retried with backoff.
func (c *Client) Do(ctx context.Context, method, path string, body []byte) (*Response, error) {
	u := *c.base
	if i := strings.IndexByte(path, '?'); i >= 0 {
		u.Path += path[:i]
		u.RawQuery = path[i+1:]
	} else {
		u.Path += path
	}
	if body == nil {
		body = []byte{}
	}

	wait := time.Second
	var lastErr error
	for attempt := 1; attempt <= c.retries; attempt++ {
		resp, err := c.once(ctx, method, u.String(), path, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var ae *APIError
		retryable := false
		if errors.As(err, &ae) {
			retryable = ae.IsRetryable()
		} else if ctx.Err() == nil {
			retryable = true // transport-level failure
		}
		if !retryable || attempt == c.retries {
			return resp, err
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		wait = min(wait*2, 30*time.Second)
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, method, fullURL, path string, body []byte) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chef: building request: %w", err)
	}
	if err := c.sign(req, body); err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.tracef("%s %s: %v (%s)", method, path, err, time.Since(start).Round(time.Millisecond))
		return nil, fmt.Errorf("chef: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	c.tracef("%s %s: %d %dB (%s)", method, path, resp.StatusCode, len(data), time.Since(start).Round(time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("chef: reading response for %s %s: %w", method, path, err)
	}
	r := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return r, &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: data}
	}
	return r, nil
}

func (c *Client) tracef(format string, args ...any) {
	if c.trace != nil {
		c.trace(format, args...)
	}
}

// Get performs a GET and decodes the JSON body into out (which may be nil).
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.json(ctx, http.MethodGet, path, nil, out)
}

// Put encodes in as JSON, PUTs it, and decodes the reply into out.
func (c *Client) Put(ctx context.Context, path string, in, out any) error {
	body, err := encode(in)
	if err != nil {
		return err
	}
	return c.json(ctx, http.MethodPut, path, body, out)
}

// Post encodes in as JSON, POSTs it, and decodes the reply into out.
func (c *Client) Post(ctx context.Context, path string, in, out any) error {
	body, err := encode(in)
	if err != nil {
		return err
	}
	return c.json(ctx, http.MethodPost, path, body, out)
}

// Delete performs a DELETE and decodes the reply into out.
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.json(ctx, http.MethodDelete, path, nil, out)
}

func encode(in any) ([]byte, error) {
	switch v := in.(type) {
	case nil:
		return nil, nil
	case []byte:
		return v, nil
	case json.RawMessage:
		return v, nil
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("chef: encoding request body: %w", err)
	}
	return body, nil
}

func (c *Client) json(ctx context.Context, method, path string, body []byte, out any) error {
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], resp.Body...)
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("chef: decoding %s %s response: %w", method, path, err)
	}
	return nil
}

// Fetch downloads an unauthenticated URL (bookshelf cookbook files are
// pre-signed) and returns the body.
func (c *Client) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chef: GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	c.tracef("GET %s: %d %dB (%s)", redactQuery(rawURL), resp.StatusCode, len(data), time.Since(start).Round(time.Millisecond))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: redactQuery(rawURL), Body: data}
	}
	return data, nil
}

// Upload PUTs raw bytes to a pre-signed URL (sandbox checksum upload).
func (c *Client) Upload(ctx context.Context, rawURL string, data []byte, contentMD5 string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", "application/x-binary")
	req.Header.Set("Content-MD5", contentMD5)
	req.ContentLength = int64(len(data))
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("chef: PUT %s: %w", redactQuery(rawURL), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	c.tracef("PUT %s: %d (%s)", redactQuery(rawURL), resp.StatusCode, time.Since(start).Round(time.Millisecond))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Method: http.MethodPut, Path: redactQuery(rawURL), Body: body}
	}
	return nil
}

// redactQuery drops the query string (which carries the pre-signature) from
// URLs before they reach logs.
func redactQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i] + "?..."
	}
	return rawURL
}
