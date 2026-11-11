package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsLoopbackHost(t *testing.T) {
	check := func(t *testing.T, host string, want bool) {
		t.Helper()
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %t; want %t", host, got, want)
		}
	}
	t.Run("IPv4 loopback", func(t *testing.T) { check(t, "127.0.0.1", true) })
	t.Run("IPv4 loopback range", func(t *testing.T) { check(t, "127.8.9.10", true) })
	t.Run("IPv6 loopback", func(t *testing.T) { check(t, "::1", true) })
	t.Run("localhost", func(t *testing.T) { check(t, "localhost", true) })
	t.Run("empty host", func(t *testing.T) { check(t, "", false) })
	t.Run("all IPv4 interfaces", func(t *testing.T) { check(t, "0.0.0.0", false) })
	t.Run("all IPv6 interfaces", func(t *testing.T) { check(t, "::", false) })
	t.Run("private IPv4", func(t *testing.T) { check(t, "192.168.1.10", false) })
	t.Run("hostname suffix", func(t *testing.T) { check(t, "localhost.example", false) })
	t.Run("uppercase localhost", func(t *testing.T) { check(t, "LOCALHOST", false) })
}

func TestListenDevPlainHTTP(t *testing.T) {
	listener, err := Listen(Config{Addr: "127.0.0.1:0", Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !strings.HasPrefix(listener.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL() = %q; want loopback HTTP URL", listener.URL())
	}
	served := make(chan error, 1)
	go func() {
		served <- listener.Serve(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(listener.URL() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.ProtoMajor != 1 || response.StatusCode != http.StatusOK || response.Header.Get("Alt-Svc") != "" {
		t.Errorf("response = %s %d, Alt-Svc %q", response.Proto, response.StatusCode, response.Header.Get("Alt-Svc"))
	}
	_ = listener.Close()
	if err := <-served; err == nil {
		t.Error("Serve returned nil after listener close")
	}
}

func TestListenDevRejectsNonLoopback(t *testing.T) {
	check := func(t *testing.T, addr string) {
		t.Helper()
		listener, err := Listen(Config{Addr: addr, Dev: true})
		if listener != nil {
			_ = listener.Close()
		}
		if !errors.Is(err, ErrPlainHTTPNotLoopback) {
			t.Errorf("Listen(%q) error = %v; want ErrPlainHTTPNotLoopback", addr, err)
		}
	}
	t.Run("all interfaces", func(t *testing.T) { check(t, "0.0.0.0:0") })
	t.Run("empty host", func(t *testing.T) { check(t, ":0") })
}

func TestListenTLSSelection(t *testing.T) {
	check := func(t *testing.T, cfg Config, want error) {
		t.Helper()
		listener, err := Listen(cfg)
		if listener != nil {
			_ = listener.Close()
		}
		if !errors.Is(err, want) {
			t.Errorf("Listen error = %v; want %v", err, want)
		}
	}
	t.Run("not dev without certificate", func(t *testing.T) { check(t, Config{Addr: "127.0.0.1:0"}, ErrTLSRequired) })
	t.Run("dev with certificate only", func(t *testing.T) {
		check(t, Config{Addr: "127.0.0.1:0", Dev: true, CertFile: "cert"}, ErrIncompleteTLS)
	})
	t.Run("not dev with certificate only", func(t *testing.T) { check(t, Config{Addr: "127.0.0.1:0", CertFile: "cert"}, ErrIncompleteTLS) })
}

func TestListenTLSSelectionWithCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM, keyPEM := testCertificate(t)
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(pemBlock(t, certPEM).Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	t.Run("dev ignores certificate", func(t *testing.T) {
		listener, err := Listen(Config{Addr: "127.0.0.1:0", Dev: true, CertFile: certPath, KeyFile: keyPath})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if listener.TLS() || !strings.HasPrefix(listener.URL(), "http://127.0.0.1:") {
			t.Fatalf("TLS() = %t, URL() = %q", listener.TLS(), listener.URL())
		}
	})
	t.Run("TLS outside dev", func(t *testing.T) {
		listener, err := Listen(Config{Addr: "127.0.0.1:0", CertFile: certPath, KeyFile: keyPath})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if !listener.TLS() || !strings.HasPrefix(listener.URL(), "https://") {
			t.Fatalf("TLS() = %t, URL() = %q", listener.TLS(), listener.URL())
		}
		served := make(chan error, 1)
		go func() {
			served <- listener.Serve(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
		}()
		response, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 3 * time.Second}).Get(listener.URL())
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.ProtoMajor != 1 || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Alt-Svc"), "h3=") {
			t.Errorf("response = %s %d, Alt-Svc %q", response.Proto, response.StatusCode, response.Header.Get("Alt-Svc"))
		}
		_ = listener.Close()
		<-served
	})
}

func TestListenerGracefulShutdown(t *testing.T) {
	listener, err := Listen(Config{Addr: "127.0.0.1:0", Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	served := make(chan error, 1)
	go func() {
		served <- listener.Serve(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(http.StatusOK) }))
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	responseCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		response, requestErr := client.Get(listener.URL())
		if requestErr != nil {
			errCh <- requestErr
			return
		}
		responseCh <- response
	}()
	<-started
	cancel()
	close(release)
	select {
	case response := <-responseCh:
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("in-flight status = %d; want 200", response.StatusCode)
		}
	case requestErr := <-errCh:
		t.Fatalf("in-flight request failed: %v", requestErr)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	if err := <-served; err != nil {
		t.Errorf("Serve after cancellation = %v; want nil", err)
	}
	newConn, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond)
	if err == nil {
		_ = newConn.Close()
		t.Error("new connection succeeded after shutdown")
	}
	_ = listener.Close()
}

func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	certificate := fixture.TLS.Certificates[0]
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func pemBlock(t *testing.T, data []byte) *pem.Block {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate PEM did not decode")
	}
	return block
}
