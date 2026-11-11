package content

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrBlockedDestination identifies a URL or resolved address denied by fetch policy.
	ErrBlockedDestination = errors.New("content: fetch destination not allowed")
	// ErrRedirectRefused identifies a redirect denied by the local client policy.
	ErrRedirectRefused = errors.New("content: redirect refused")
	// ErrResponseTooLarge identifies a response exceeding the caller's byte limit.
	ErrResponseTooLarge = errors.New("content: response exceeds limit")
)

const (
	// DefaultMaxRedirects bounds redirects when FetchConfig.MaxRedirects is zero.
	DefaultMaxRedirects = 3
	// DefaultFetchTimeout bounds a request when FetchConfig.Timeout is zero.
	DefaultFetchTimeout = 30 * time.Second
	// HintStaleAfter is the default age after which a host hint is stale.
	HintStaleAfter = 7 * 24 * time.Hour
)

var fetchBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var fetchNAT64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// Resolver is satisfied by *net.Resolver.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DestinationPolicy allows explicitly configured destinations that the default policy blocks.
type DestinationPolicy struct {
	// Allow contains operator-configured prefixes that may override the default
	// address blocks. It is empty by default, is never filled implicitly by dev
	// mode, and must not be populated from a remote hint, URL, or post.
	Allow []netip.Prefix
	// Resolver resolves hostnames; nil uses net.DefaultResolver.
	Resolver Resolver
}

// FetchConfig configures isolated HTTPS fetching.
type FetchConfig struct {
	// Policy applies address checks to each connection.
	Policy DestinationPolicy
	// MaxRedirects is the redirect limit; zero uses DefaultMaxRedirects.
	MaxRedirects int
	// Timeout is the total request timeout; zero uses DefaultFetchTimeout.
	Timeout time.Duration
	// AllowRedirectOrigin approves redirects to a new origin; nil refuses them.
	AllowRedirectOrigin func(origin string) bool
	// RootCAs supplies TLS trust roots; nil uses the system roots.
	RootCAs *x509.CertPool
}

func fetchEffectiveAddr(a netip.Addr) netip.Addr {
	if !a.IsValid() || a.Zone() != "" {
		return netip.Addr{}
	}
	if a.Is4In6() {
		a = a.Unmap()
	}
	if fetchNAT64Prefix.Contains(a) {
		bytes := a.As16()
		a = netip.AddrFrom4([4]byte{bytes[12], bytes[13], bytes[14], bytes[15]})
	}
	return a
}

// IsFetchableAddress reports whether an address is globally reachable by default.
func IsFetchableAddress(a netip.Addr) bool {
	a = fetchEffectiveAddr(a)
	if !a.IsValid() {
		return false
	}
	if a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range fetchBlockedPrefixes {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

// Fetchable reports whether a is globally reachable or matches an operator allowlist prefix.
func (p DestinationPolicy) Fetchable(a netip.Addr) bool {
	a = fetchEffectiveAddr(a)
	if !a.IsValid() {
		return false
	}
	if IsFetchableAddress(a) {
		return true
	}
	for _, prefix := range p.Allow {
		if prefix.IsValid() && prefix.Contains(a) {
			return true
		}
	}
	return false
}

// DialContext resolves names once and binds an approved address to the socket.
func (p DestinationPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("%w: unsupported network %q", ErrBlockedDestination, network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid address: %v", ErrBlockedDestination, err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("%w: invalid port", ErrBlockedDestination)
	}
	var addresses []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{literal}
	} else {
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		addresses, err = resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", host, err)
		}
	}

	dialer := net.Dialer{Control: func(controlNetwork, controlAddress string, _ syscall.RawConn) error {
		controlHost, _, splitErr := net.SplitHostPort(controlAddress)
		if splitErr != nil {
			return fmt.Errorf("%w: invalid socket address", ErrBlockedDestination)
		}
		remote, parseErr := netip.ParseAddr(controlHost)
		if parseErr != nil || !p.Fetchable(remote) {
			return fmt.Errorf("%w: socket address %q", ErrBlockedDestination, controlHost)
		}
		return nil
	}}
	var lastErr error
	var fetchable bool
	for _, candidate := range addresses {
		if network == "tcp4" && !candidate.Is4() && !candidate.Is4In6() {
			continue
		}
		if network == "tcp6" && candidate.Is4() {
			continue
		}
		if !p.Fetchable(candidate) {
			continue
		}
		fetchable = true
		if network != "tcp6" {
			candidate = candidate.Unmap()
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), strconv.Itoa(int(portNumber))))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if !fetchable {
		return nil, fmt.Errorf("%w: no fetchable address for %q", ErrBlockedDestination, host)
	}
	return nil, lastErr
}

// NewFetchClient returns an isolated HTTPS client governed by cfg.Policy.
func NewFetchClient(cfg FetchConfig) *http.Client {
	maxRedirects := cfg.MaxRedirects
	if maxRedirects == 0 {
		maxRedirects = DefaultMaxRedirects
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultFetchTimeout
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            cfg.Policy.DialContext,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 * 1024,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects || request.URL.Scheme != "https" || request.URL.User != nil {
				return fmt.Errorf("%w: disallowed target", ErrRedirectRefused)
			}
			original, err := Origin(via[0].URL)
			if err != nil {
				return fmt.Errorf("%w: invalid original origin", ErrRedirectRefused)
			}
			target, err := Origin(request.URL)
			if err != nil {
				return fmt.Errorf("%w: invalid target origin", ErrRedirectRefused)
			}
			if original != target && (cfg.AllowRedirectOrigin == nil || !cfg.AllowRedirectOrigin(target)) {
				return fmt.Errorf("%w: origin %s requires consent", ErrRedirectRefused, target)
			}
			request.Header.Del("Referer")
			request.Header.Del("Cookie")
			request.Header.Del("Authorization")
			return nil
		},
	}
}

// NewFetchRequest constructs a credential-free HTTPS GET request.
func NewFetchRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("%w: invalid HTTPS URL", ErrBlockedDestination)
	}
	u.Fragment = ""
	u.RawFragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid HTTPS URL: %v", ErrBlockedDestination, err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	return request, nil
}

// Origin returns the canonical consent key for an HTTPS URL.
func Origin(u *url.URL) (string, error) {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return "", errors.New("content: invalid HTTPS origin")
	}
	parsedAuthority, err := url.Parse("https://" + u.Host)
	if err != nil || parsedAuthority.User != nil || parsedAuthority.Host != u.Host {
		return "", errors.New("content: invalid HTTPS origin")
	}
	host := strings.ToLower(parsedAuthority.Hostname())
	if host == "" {
		return "", errors.New("content: invalid HTTPS origin")
	}
	port := parsedAuthority.Port()
	if port == "" {
		port = "443"
	} else {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", errors.New("content: invalid HTTPS origin")
		}
		port = strconv.FormatUint(portNumber, 10)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host + ":" + port, nil
}

// ReadLimited reads at most limit bytes and reports ErrResponseTooLarge if more are available.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, ErrResponseTooLarge
	}
	if limit == math.MaxInt64 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}

// HintStale reports whether a locally observed hint is more than seven days old.
// observedAt must be locally recorded or independently authenticated; a remote unauthenticated timestamp is not acceptable.
func HintStale(observedAt, now time.Time) bool {
	return now.Sub(observedAt) > HintStaleAfter
}
