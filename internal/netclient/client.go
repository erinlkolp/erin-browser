package netclient

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

//go:embed certs/ca-certificates.crt
var embeddedCACerts []byte

// MaxResponseBytes caps body downloads to protect 256 MB RAM devices like the OLPC XO-1.
const MaxResponseBytes = 6 * 1024 * 1024

// SearchEngine defines a built-in search shortcut.
type SearchEngine struct {
	ID          string
	Name        string
	Prefix      string
	URLTemplate string // Must contain %s for url.QueryEscape(query)
}

// SearchEngines lists available built-in search engines in cycle order.
var SearchEngines = []SearchEngine{
	{
		ID:          "ddg",
		Name:        "DuckDuckGo Lite",
		Prefix:      "d:",
		URLTemplate: "https://lite.duckduckgo.com/lite/?q=%s",
	},
	{
		ID:          "wiki",
		Name:        "Wikipedia",
		Prefix:      "w:",
		URLTemplate: "https://en.wikipedia.org/w/index.php?search=%s",
	},
	{
		ID:          "mojeek",
		Name:        "Mojeek",
		Prefix:      "m:",
		URLTemplate: "https://www.mojeek.com/search?q=%s",
	},
	{
		ID:          "frogfind",
		Name:        "FrogFind",
		Prefix:      "f:",
		URLTemplate: "http://frogfind.com/?q=%s",
	},
	{
		ID:          "google",
		Name:        "Google (requires JS on google.com)",
		Prefix:      "g:",
		URLTemplate: "https://www.google.com/search?q=%s&gbv=1",
	},
}

// UserAgentProfile defines selectable browser User-Agent presets.
type UserAgentProfile struct {
	ID    string
	Label string
	Value string
}

// UserAgentProfiles lists selectable UA strings.
var UserAgentProfiles = []UserAgentProfile{
	{
		ID:    "firefox",
		Label: "Firefox Linux",
		Value: "Mozilla/5.0 (X11; Linux i686; rv:136.0) Gecko/20100101 Firefox/136.0",
	},
	{
		ID:    "mobile",
		Label: "Mobile Web",
		Value: "Mozilla/5.0 (Linux; Android 14; Mobile) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Mobile Safari/537.36",
	},
	{
		ID:    "classic",
		Label: "Erin XO-1",
		Value: "Erin/1.0 (OLPC XO-1; Linux i586)",
	},
}

// Response holds a fetched and decoded web page.
type Response struct {
	FinalURL    string
	StatusCode  int
	Status      string
	ContentType string
	IsHTML      bool
	IsTLS       bool
	TLSVersion  string
	Body        string
	Truncated   bool
}

// Client wraps net/http with modern TLS 1.2/1.3, embedded CA certs, cookies,
// gzip/deflate/brotli decompression, and charset decoding.
type Client struct {
	httpClient *http.Client
	uaProfile  UserAgentProfile
	engine     SearchEngine
}

// New creates a self-contained HTTP/HTTPS client that never depends on the OS CA store.
func New(engineID, uaID string) (*Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(embeddedCACerts) {
		return nil, errors.New("failed to parse embedded CA certificates")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		DisableCompression:    true, // We handle gzip, deflate, and brotli ourselves.
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}

	c := &Client{
		httpClient: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   30 * time.Second,
		},
		uaProfile: findUA(uaID),
		engine:    findEngine(engineID),
	}
	return c, nil
}

func findEngine(id string) SearchEngine {
	for _, e := range SearchEngines {
		if e.ID == id {
			return e
		}
	}
	return SearchEngines[0]
}

func findUA(id string) UserAgentProfile {
	for _, u := range UserAgentProfiles {
		if u.ID == id {
			return u
		}
	}
	return UserAgentProfiles[0]
}

func (c *Client) SearchEngine() SearchEngine { return c.engine }

func (c *Client) SetSearchEngine(id string) SearchEngine {
	c.engine = findEngine(id)
	return c.engine
}

func (c *Client) CycleSearchEngine() SearchEngine {
	for i, e := range SearchEngines {
		if e.ID == c.engine.ID {
			c.engine = SearchEngines[(i+1)%len(SearchEngines)]
			return c.engine
		}
	}
	c.engine = SearchEngines[0]
	return c.engine
}

func (c *Client) UserAgent() UserAgentProfile { return c.uaProfile }

func (c *Client) SetUserAgent(id string) UserAgentProfile {
	c.uaProfile = findUA(id)
	return c.uaProfile
}

func (c *Client) CycleUserAgent() UserAgentProfile {
	for i, u := range UserAgentProfiles {
		if u.ID == c.uaProfile.ID {
			c.uaProfile = UserAgentProfiles[(i+1)%len(UserAgentProfiles)]
			return c.uaProfile
		}
	}
	c.uaProfile = UserAgentProfiles[0]
	return c.uaProfile
}

// ResolveInput converts user input from the URL/search bar into a target URL.
// Supports explicit URLs, bare hostnames, about:* pages, bang prefixes (w:, d:, m:, f:, g:),
// and free-text search queries.
func (c *Client) ResolveInput(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "about:") {
		return lower
	}

	// Check engine prefix shortcuts like "w: olpc xo-1" or "d: fedora 18".
	for _, eng := range SearchEngines {
		if strings.HasPrefix(lower, eng.Prefix) {
			q := strings.TrimSpace(s[len(eng.Prefix):])
			if q != "" {
				return fmt.Sprintf(eng.URLTemplate, url.QueryEscape(q))
			}
		}
	}

	// Explicit http/https scheme.
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return s
	}

	// Bare domain or localhost/IP without spaces.
	if !strings.ContainsAny(s, " \t\n\r") && looksLikeHost(s) {
		if strings.HasPrefix(lower, "localhost") || strings.HasPrefix(lower, "127.") || strings.HasPrefix(lower, "192.168.") || strings.HasPrefix(lower, "10.") {
			return "http://" + s
		}
		return "https://" + s
	}

	// Otherwise treat as a search query using the active search engine.
	return fmt.Sprintf(c.engine.URLTemplate, url.QueryEscape(s))
}

func looksLikeHost(s string) bool {
	hostPart := s
	if idx := strings.IndexByte(hostPart, '/'); idx >= 0 {
		hostPart = hostPart[:idx]
	}
	if strings.HasPrefix(strings.ToLower(hostPart), "localhost") {
		return true
	}
	if !strings.Contains(hostPart, ".") {
		return false
	}
	// Strip optional :port
	if h, _, err := net.SplitHostPort(hostPart); err == nil {
		hostPart = h
	}
	if net.ParseIP(hostPart) != nil {
		return true
	}
	parts := strings.Split(hostPart, ".")
	tld := parts[len(parts)-1]
	if len(tld) < 2 || len(tld) > 24 {
		return false
	}
	for _, r := range tld {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// Get fetches a URL via HTTP GET.
func (c *Client) Get(rawURL, referer string) (*Response, error) {
	return c.doRequest(http.MethodGet, rawURL, nil, referer)
}

// PostForm submits an application/x-www-form-urlencoded POST request.
func (c *Client) PostForm(rawURL string, values url.Values, referer string) (*Response, error) {
	return c.doRequest(http.MethodPost, rawURL, values, referer)
}

func (c *Client) doRequest(method, rawURL string, form url.Values, referer string) (*Response, error) {
	var bodyReader io.Reader
	if method == http.MethodPost && form != nil {
		bodyReader = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequest(method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.uaProfile.Value)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" && strings.HasPrefix(referer, "http") {
		req.Header.Set("Referer", referer)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	reader, cleanup, err := decodeContentEncoding(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("decompress response: %w", err)
	}
	defer cleanup()

	limited := io.LimitReader(reader, MaxResponseBytes+1)
	rawBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	truncated := false
	if len(rawBytes) > MaxResponseBytes {
		rawBytes = rawBytes[:MaxResponseBytes]
		truncated = true
	}

	ctHeader := resp.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(ctHeader)
	mediaType = strings.ToLower(mediaType)
	if mediaType == "" {
		mediaType = strings.ToLower(http.DetectContentType(rawBytes))
		if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
			mediaType = strings.TrimSpace(mediaType[:idx])
		}
	}

	isHTML := mediaType == "text/html" || mediaType == "application/xhtml+xml"
	charset := strings.TrimSpace(params["charset"])
	if charset == "" && isHTML {
		charset = sniffHTMLCharset(rawBytes)
	}

	bodyStr := decodeCharset(rawBytes, charset)

	tlsVer := ""
	isTLS := false
	if resp.TLS != nil {
		isTLS = true
		switch resp.TLS.Version {
		case tls.VersionTLS13:
			tlsVer = "TLS 1.3"
		case tls.VersionTLS12:
			tlsVer = "TLS 1.2"
		default:
			tlsVer = "TLS"
		}
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return &Response{
		FinalURL:    finalURL,
		StatusCode:  resp.StatusCode,
		Status:      resp.Status,
		ContentType: mediaType,
		IsHTML:      isHTML,
		IsTLS:       isTLS,
		TLSVersion:  tlsVer,
		Body:        bodyStr,
		Truncated:   truncated,
	}, nil
}

func decodeContentEncoding(r io.Reader, encoding string) (io.Reader, func(), error) {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	noop := func() {}
	switch enc {
	case "", "identity":
		return r, noop, nil
	case "gzip":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, noop, err
		}
		return gr, func() { _ = gr.Close() }, nil
	case "br":
		return brotli.NewReader(r), noop, nil
	case "deflate":
		// Some servers send zlib-wrapped deflate, others send raw deflate.
		buf, err := io.ReadAll(io.LimitReader(r, MaxResponseBytes+1))
		if err != nil {
			return nil, noop, err
		}
		if zr, err := zlib.NewReader(bytes.NewReader(buf)); err == nil {
			return zr, func() { _ = zr.Close() }, nil
		}
		fr := flate.NewReader(bytes.NewReader(buf))
		return fr, func() { _ = fr.Close() }, nil
	default:
		return r, noop, nil
	}
}

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-zA-Z0-9._-]+)`)

func sniffHTMLCharset(data []byte) string {
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	m := metaCharsetRe.FindSubmatch(sample)
	if len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

func decodeCharset(data []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs == "" || cs == "utf-8" || cs == "utf8" || cs == "us-ascii" || cs == "ascii" {
		return string(data)
	}
	enc, err := htmlindex.Get(cs)
	if err != nil || enc == nil {
		return string(data)
	}
	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return string(data)
	}
	return string(decoded)
}
