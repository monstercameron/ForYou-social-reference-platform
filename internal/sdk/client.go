package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const rdDefaultTimeout = 15 * time.Second
const rdObjectLimit int64 = 1 << 20
const rdPageLimit int64 = 2 << 20
const rdErrorLimit int64 = 1024

// Options configures a client pinned to one service identity.
type Options struct {
	// ServiceID is the pinned operator did:key of the endpoint.
	ServiceID string
	// AllowLoopbackHTTP permits HTTP only for localhost and loopback IP literals.
	AllowLoopbackHTTP bool
	// HTTPClient supplies a transport; the SDK replaces its jar and redirect policy.
	HTTPClient *http.Client
	// Timeout overrides the default request timeout when positive.
	Timeout time.Duration
	// Now supplies the only clock read by the SDK; nil uses time.Now.
	Now func() time.Time
}

// Client is a bounded, credential-free HTTP client for one ForYou service.
type Client struct {
	base      *url.URL
	serviceID string
	http      *http.Client
	now       func() time.Time
}

// New validates a service URL and creates a client that never follows redirects.
func New(baseURL string, opts Options) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("sdk: invalid base URL")
	}
	if _, err := keys.ParseDID(opts.ServiceID); err != nil {
		return nil, errors.New("sdk: ServiceID must be a canonical did:key")
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || !opts.AllowLoopbackHTTP || !rdIsLoopbackHost(u.Hostname()) {
			return nil, errors.New("sdk: base URL must use HTTPS, or explicitly allowed loopback HTTP")
		}
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.RawPath, "..") {
		return nil, errors.New("sdk: invalid base path")
	}
	if u.Path != "" && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = rdDefaultTimeout
	}
	if timeout < 0 {
		return nil, errors.New("sdk: timeout must be positive")
	}
	clone := *hc
	clone.Jar = nil
	clone.Timeout = timeout
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{base: u, serviceID: opts.ServiceID, http: &clone, now: now}, nil
}

// BaseURL returns the normalized service base URL.
func (c *Client) BaseURL() string { return c.base.String() }

// ServiceID returns the configured pinned operator DID.
func (c *Client) ServiceID() string { return c.serviceID }

type response struct {
	status int
	header http.Header
	body   []byte
}

// send performs one request. path is already escaped and is appended to the base path.
// A 2xx returns the response; any other status returns *Error; transport failures return a wrapped error.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, header http.Header, limit int64) (response, error) {
	target := *c.base
	basePath := strings.TrimSuffix(target.EscapedPath(), "/")
	target.RawPath = basePath + "/" + strings.TrimPrefix(path, "/")
	decoded, err := url.PathUnescape(target.RawPath)
	if err != nil {
		return response{}, fmt.Errorf("sdk: invalid escaped path: %w", err)
	}
	target.Path = decoded
	target.RawQuery = rdEncodeQuery(query)
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), requestBody)
	if err != nil {
		return response{}, fmt.Errorf("sdk: create request: %w", err)
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Del("Cookie")
	req.Header.Del("Referer")
	res, err := c.http.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("sdk: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	encoding := res.Header.Get("Content-Encoding")
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		return response{}, rdProtocolHTTP(res.StatusCode, "unsupported content encoding")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		limited, readErr := io.ReadAll(io.LimitReader(res.Body, rdErrorLimit+1))
		if readErr != nil {
			return response{}, fmt.Errorf("sdk: read error response: %w", readErr)
		}
		if int64(len(limited)) > rdErrorLimit {
			return response{}, rdProtocolHTTP(res.StatusCode, "error body exceeds 1024 bytes")
		}
		if _, parseErr := canon.Parse(limited, canon.Limits{MaxBytes: int(rdErrorLimit), MaxDepth: canon.MessageLimits.MaxDepth}); parseErr != nil {
			return response{}, rdProtocolHTTP(res.StatusCode, "invalid public error JSON")
		}
		parsed, parseErr := apierr.Parse(limited)
		if parseErr != nil {
			return response{}, rdProtocolHTTP(res.StatusCode, "invalid public error body")
		}
		code, ok := parsed.Status.HTTPStatus()
		if !ok || code != res.StatusCode {
			return response{}, rdProtocolHTTP(res.StatusCode, "HTTP status does not match public error")
		}
		return response{}, &Error{HTTPStatus: res.StatusCode, Status: string(parsed.Status), RetryAfter: time.Duration(parsed.RetryAfterSeconds) * time.Second, Endpoint: path}
	}
	mediaType := strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0])
	wanted := "application/json"
	if accept := header.Get("Accept"); accept != "" {
		wanted = accept
	}
	if mediaType != wanted {
		return response{}, rdProtocolHTTP(res.StatusCode, "success response is not "+wanted)
	}
	if limit <= 0 {
		limit = rdObjectLimit
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return response{}, rdProtocolHTTPCause(res.StatusCode, err)
	}
	if int64(len(data)) > limit {
		return response{}, rdProtocolHTTP(res.StatusCode, "response body exceeds limit")
	}
	return response{status: res.StatusCode, header: res.Header.Clone(), body: data}, nil
}

// getJSON strictly decodes one JSON response and returns its exact bytes.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, limit int64, out any) ([]byte, error) {
	res, err := c.send(ctx, http.MethodGet, path, query, nil, nil, limit)
	if err != nil {
		return nil, err
	}
	if res.status != http.StatusOK {
		return nil, rdProtocol("unexpected success status")
	}
	if raw, ok := out.(*json.RawMessage); ok {
		if _, err := canon.Parse(res.body, canon.Limits{MaxBytes: int(limit), MaxDepth: canon.MessageLimits.MaxDepth}); err != nil {
			return nil, rdProtocol("invalid JSON response")
		}
		*raw = append((*raw)[:0], res.body...)
		return append([]byte(nil), res.body...), nil
	}
	definition := rdDefinition(out)
	if definition == "" {
		return nil, rdProtocol("missing wire schema for response type")
	}
	if err := wire.DecodeLimits(res.body, canon.Limits{MaxBytes: int(limit), MaxDepth: canon.MessageLimits.MaxDepth}, definition, out); err != nil {
		return nil, rdProtocol("invalid JSON response")
	}
	return append([]byte(nil), res.body...), nil
}

func rdDefinition(out any) string {
	switch out.(type) {
	case *wire.TransportMessage:
		return "TransportMessage"
	case *wire.DiscoveryPage:
		return "DiscoveryPage"
	case *wire.ChangesPage:
		return "ChangesPage"
	case *wire.RetentionDeclarationsPage:
		return "RetentionDeclarationsPage"
	case *wire.KeySuccession:
		return "KeySuccession"
	case *wire.OperatorManifest:
		return "OperatorManifest"
	case *wire.SettlementProof:
		return "SettlementProof"
	case *wire.SettlementClaimsPage:
		return "SettlementClaimsPage"
	case *wire.SettlementLeavesPage:
		return "SettlementLeavesPage"
	case *wire.SettlementWitnessesPage:
		return "SettlementWitnessesPage"
	case *wire.GenesisManifest:
		return "GenesisManifest"
	case *wire.FoundationCertificatesPage:
		return "FoundationCertificatesPage"
	case *wire.FeeSchedulesPage:
		return "FeeSchedulesPage"
	case *wire.DistributionPage:
		return "DistributionPage"
	case *wire.VaultStatusPage:
		return "VaultStatusPage"
	case *wire.FoundationLogPage:
		return "FoundationLogPage"
	case *wire.FoundationLogHeadsPage:
		return "FoundationLogHeadsPage"
	case *wire.FoundationLogProof:
		return "FoundationLogProof"
	case *wire.VaultReceipt:
		return "VaultReceipt"
	case *wire.MonitorWitnessesPage:
		return "MonitorWitnessesPage"
	case *wire.EquivocationsPage:
		return "EquivocationsPage"
	case *wire.QualityReportsPage:
		return "QualityReportsPage"
	default:
		return ""
	}
}

func rdIsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func rdEncodeQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		vals := append([]string(nil), values[key]...)
		sort.Strings(vals)
		for _, value := range vals {
			parts = append(parts, rdEscape(key)+"="+rdEscape(value))
		}
	}
	return strings.Join(parts, "&")
}
func rdEscape(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
