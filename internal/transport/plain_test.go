package transport

import (
	"io"
	"net"
	"net/http"
	"testing"
)

func TestPlainHTTPIsLoopbackOnly(t *testing.T) {
	check := func(t *testing.T, addr string, want bool) {
		t.Helper()
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %t; want %t", addr, got, want)
		}
	}
	t.Run("IPv4 loopback", func(t *testing.T) { check(t, "127.0.0.1:8443", true) })
	t.Run("IPv6 loopback", func(t *testing.T) { check(t, "[::1]:8443", true) })
	t.Run("localhost name", func(t *testing.T) { check(t, "localhost:8443", true) })
	t.Run("all interfaces", func(t *testing.T) { check(t, "0.0.0.0:8443", false) })
	t.Run("empty host", func(t *testing.T) { check(t, ":8443", false) })
	t.Run("private network address", func(t *testing.T) { check(t, "192.168.1.10:8443", false) })
	t.Run("missing port", func(t *testing.T) { check(t, "127.0.0.1", false) })

	t.Run("refuses to listen beyond loopback", func(t *testing.T) {
		if err := ListenAndServePlain("0.0.0.0:0", http.NotFoundHandler()); err == nil {
			t.Fatal("ListenAndServePlain on all interfaces returned nil; want an error")
		}
	})
}

func TestPlainHTTPServesTheHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go servePlain(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "dev")
	}))
	response, err := http.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "dev" {
		t.Errorf("body = %q; want %q", body, "dev")
	}
}
