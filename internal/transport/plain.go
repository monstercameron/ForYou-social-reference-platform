package transport

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// ListenAndServePlain serves HTTP/1.1 without TLS for dev mode. It refuses any
// address that is not loopback, so an unencrypted listener is never reachable
// from another machine.
func ListenAndServePlain(addr string, handler http.Handler) error {
	if !IsLoopback(addr) {
		return fmt.Errorf("plain HTTP is limited to loopback addresses, not %q", addr)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen TCP: %w", err)
	}
	defer listener.Close()
	return servePlain(listener, handler)
}

// IsLoopback reports whether a host:port address names this machine only.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func servePlain(listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	return fmt.Errorf("HTTP server stopped: %w", server.Serve(listener))
}
