package content

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fetchResolver struct {
	mu       sync.Mutex
	answers  []netip.Addr
	sequence [][]netip.Addr
	calls    int
}

func (r *fetchResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.sequence) > 0 {
		index := r.calls - 1
		if index >= len(r.sequence) {
			index = len(r.sequence) - 1
		}
		return r.sequence[index], nil
	}
	return r.answers, nil
}

func (r *fetchResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func fetchTestServer(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return server, pool
}

func fetchTestPort(t *testing.T, server *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Port()
}

func fetchTestClient(pool *x509.CertPool, resolver Resolver, allowRedirect func(string) bool) *http.Client {
	return NewFetchClient(FetchConfig{
		Policy:              DestinationPolicy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Resolver: resolver},
		RootCAs:             pool,
		AllowRedirectOrigin: allowRedirect,
	})
}

func TestIsFetchableAddress(t *testing.T) {
	check := func(t *testing.T, input string, want bool) {
		t.Helper()
		address, err := netip.ParseAddr(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := IsFetchableAddress(address); got != want {
			t.Errorf("IsFetchableAddress(%q) = %t; want %t", input, got, want)
		}
	}
	t.Run("loopback", func(t *testing.T) { check(t, "127.0.0.1", false) })
	t.Run("private 10 range", func(t *testing.T) { check(t, "10.0.0.5", false) })
	t.Run("private 192 range", func(t *testing.T) { check(t, "192.168.1.1", false) })
	t.Run("private 172 range", func(t *testing.T) { check(t, "172.16.0.1", false) })
	t.Run("link local", func(t *testing.T) { check(t, "169.254.1.1", false) })
	t.Run("unspecified", func(t *testing.T) { check(t, "0.0.0.0", false) })
	t.Run("multicast", func(t *testing.T) { check(t, "224.0.0.1", false) })
	t.Run("IPv6 loopback", func(t *testing.T) { check(t, "::1", false) })
	t.Run("IPv6 link local", func(t *testing.T) { check(t, "fe80::1", false) })
	t.Run("IPv6 unique local", func(t *testing.T) { check(t, "fc00::1", false) })
	t.Run("mapped loopback", func(t *testing.T) { check(t, "::ffff:127.0.0.1", false) })
	t.Run("mapped private", func(t *testing.T) { check(t, "::ffff:10.0.0.1", false) })
	t.Run("shared address space", func(t *testing.T) { check(t, "100.64.0.1", false) })
	t.Run("NAT64 private embedded address", func(t *testing.T) { check(t, "64:ff9b::a00:1", false) })
	t.Run("documentation IPv6", func(t *testing.T) { check(t, "2001:db8::1", false) })
	t.Run("limited broadcast", func(t *testing.T) { check(t, "255.255.255.255", false) })
	t.Run("global IPv4", func(t *testing.T) { check(t, "93.184.216.34", true) })
	t.Run("global IPv6", func(t *testing.T) { check(t, "2606:2800:220:1:248:1893:25c8:1946", true) })
	t.Run("IPv6 zone", func(t *testing.T) { check(t, "fe80::1%eth0", false) })
}

func TestDestinationPolicyAllowlist(t *testing.T) {
	address := netip.MustParseAddr("127.0.0.1")
	t.Run("empty by default", func(t *testing.T) {
		if (DestinationPolicy{}).Fetchable(address) {
			t.Fatal("loopback address was fetchable without operator opt-in")
		}
	})
	t.Run("explicit operator prefix", func(t *testing.T) {
		policy := DestinationPolicy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
		if !policy.Fetchable(address) {
			t.Fatal("operator allowlist did not permit its address")
		}
	})
}

func TestDestinationPolicyDialContextBlocksBeforeConnecting(t *testing.T) {
	resolver := &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	_, err := (DestinationPolicy{Resolver: resolver}).DialContext(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("DialContext error = %v; want ErrBlockedDestination", err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver called %d times; want 1", resolver.callCount())
	}
}

func TestNewFetchClientPinsResolvedAddress(t *testing.T) {
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	resolver := &fetchResolver{sequence: [][]netip.Addr{{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("10.0.0.1")}}}
	client := fetchTestClient(roots, resolver, nil)
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if resolver.callCount() != 1 {
		t.Fatalf("resolver called %d times; want one lookup for the connection", resolver.callCount())
	}
}

func TestNewFetchClientUsesOnlyFetchableMixedAnswer(t *testing.T) {
	server, _ := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	resolver := &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("127.0.0.1")}}
	policy := DestinationPolicy{
		Allow:    []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		Resolver: resolver,
	}
	connection, err := policy.DialContext(context.Background(), "tcp", "example.com:"+fetchTestPort(t, server))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if got, want := connection.RemoteAddr().String(), "127.0.0.1:"+fetchTestPort(t, server); got != want {
		t.Fatalf("remote address = %q; want %q", got, want)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver called %d times; want 1", resolver.callCount())
	}
}

func TestNewFetchRequestHeadersAndFragment(t *testing.T) {
	seen := false
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.URL.RequestURI() != "/a" {
			t.Errorf("request URI = %q; want /a", r.URL.RequestURI())
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q; want identity", r.Header.Get("Accept-Encoding"))
		}
		if r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("credential or referrer headers were sent: %v", r.Header)
		}
	}))
	client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, nil)
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/a#frag")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !seen {
		t.Fatal("server did not receive request")
	}
}

func TestNewFetchClientSameOriginRedirectDropsCredentials(t *testing.T) {
	secondRequest := false
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			if r.Header.Get("Cookie") != "session=secret" {
				t.Errorf("initial Cookie = %q; want test credential", r.Header.Get("Cookie"))
			}
			http.SetCookie(w, &http.Cookie{Name: "server", Value: "value"})
			http.Redirect(w, r, "/finish", http.StatusFound)
			return
		}
		secondRequest = true
		if r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("redirect forwarded credentials or referrer: %v", r.Header)
		}
	}))
	client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, nil)
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/start")
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Cookie", "session=secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !secondRequest {
		t.Fatal("same-origin redirect was not followed")
	}
}

func TestNewFetchClientRefusesHTTPRedirect(t *testing.T) {
	var server *httptest.Server
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com:"+fetchTestPort(t, server)+"/", http.StatusFound)
	}))
	client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, func(string) bool { return true })
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("client error = %v; want ErrRedirectRefused", err)
	}
}

func TestNewFetchClientRefusesRedirectUserinfo(t *testing.T) {
	var server *httptest.Server
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://user@example.com:"+fetchTestPort(t, server)+"/", http.StatusFound)
	}))
	client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, func(string) bool { return true })
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("client error = %v; want ErrRedirectRefused", err)
	}
}

func TestNewFetchClientRequiresCrossOriginConsent(t *testing.T) {
	t.Run("nil consent refuses", func(t *testing.T) {
		var server *httptest.Server
		server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://127.0.0.1:"+fetchTestPort(t, server)+"/finish", http.StatusFound)
		}))
		client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, nil)
		request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/start")
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		if !errors.Is(err, ErrRedirectRefused) {
			t.Fatalf("client error = %v; want ErrRedirectRefused", err)
		}
	})
	t.Run("approved target origin follows", func(t *testing.T) {
		var server *httptest.Server
		server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, "https://127.0.0.1:"+fetchTestPort(t, server)+"/finish", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "ok")
		}))
		port := fetchTestPort(t, server)
		targetOrigin := "https://127.0.0.1:" + port
		client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, func(origin string) bool {
			return origin == targetOrigin
		})
		request, err := NewFetchRequest(context.Background(), "https://example.com:"+port+"/start")
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	})
}

func TestNewFetchClientDefaultRedirectLimit(t *testing.T) {
	server, roots := fetchTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/0":
			http.Redirect(w, r, "/1", http.StatusFound)
		case "/1":
			http.Redirect(w, r, "/2", http.StatusFound)
		case "/2":
			http.Redirect(w, r, "/3", http.StatusFound)
		case "/3":
			http.Redirect(w, r, "/4", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "unexpectedly followed")
		}
	}))
	client := fetchTestClient(roots, &fetchResolver{answers: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, nil)
	request, err := NewFetchRequest(context.Background(), "https://example.com:"+fetchTestPort(t, server)+"/0")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("client error = %v; want ErrRedirectRefused after three redirects", err)
	}
}

func TestOrigin(t *testing.T) {
	check := func(t *testing.T, input, want string) {
		t.Helper()
		parsed, err := url.Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Origin(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("Origin(%q) = %q; want %q", input, got, want)
		}
	}
	t.Run("default HTTPS port", func(t *testing.T) { check(t, "https://Example.com/a", "https://example.com:443") })
	t.Run("IPv6 explicit port", func(t *testing.T) { check(t, "https://[2001:db8::1]:8443/", "https://[2001:db8::1]:8443") })
	t.Run("HTTP rejected", func(t *testing.T) {
		parsed, err := url.Parse("http://example.com/")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Origin(parsed); err == nil {
			t.Fatal("Origin accepted HTTP URL")
		}
	})
}

func TestReadLimited(t *testing.T) {
	t.Run("limit exact length", func(t *testing.T) {
		got, err := ReadLimited(strings.NewReader("0123456789"), 10)
		if err != nil || string(got) != "0123456789" {
			t.Fatalf("ReadLimited() = %q, %v; want exact bytes", got, err)
		}
	})
	t.Run("one extra byte", func(t *testing.T) {
		got, err := ReadLimited(strings.NewReader("01234567890"), 10)
		if !errors.Is(err, ErrResponseTooLarge) || got != nil {
			t.Fatalf("ReadLimited() = %q, %v; want ErrResponseTooLarge", got, err)
		}
	})
}

func TestHintStale(t *testing.T) {
	observed := time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)
	t.Run("exactly seven days", func(t *testing.T) {
		if HintStale(observed, observed.Add(7*24*time.Hour)) {
			t.Fatal("hint exactly seven days old was stale")
		}
	})
	t.Run("seven days and one second", func(t *testing.T) {
		if !HintStale(observed, observed.Add(7*24*time.Hour+time.Second)) {
			t.Fatal("hint more than seven days old was not stale")
		}
	})
}

func TestNewFetchRequestRejectsUnsafeURL(t *testing.T) {
	t.Run("HTTP scheme", func(t *testing.T) {
		if _, err := NewFetchRequest(context.Background(), "http://example.com/"); !errors.Is(err, ErrBlockedDestination) {
			t.Fatalf("NewFetchRequest error = %v; want ErrBlockedDestination", err)
		}
	})
	t.Run("embedded credentials", func(t *testing.T) {
		if _, err := NewFetchRequest(context.Background(), "https://user@example.com/"); !errors.Is(err, ErrBlockedDestination) {
			t.Fatalf("NewFetchRequest error = %v; want ErrBlockedDestination", err)
		}
	})
}
