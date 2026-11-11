// Package transport serves the same handler over HTTP/3 and HTTP/1.1.
package transport

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	h3Unavailable int32 = iota
	h3Available
	h3Failed
)

// ListenAndServe serves HTTP/1.1 over TCP and HTTP/3 over UDP when available.
func ListenAndServe(addr, certFile, keyFile string, handler http.Handler) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen TCP: %w", err)
	}
	defer tcp.Close()
	return serveWithUDP(tcp, cert, handler)
}

func serveWithUDP(tcp net.Listener, cert tls.Certificate, handler http.Handler) error {
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		log.Printf("HTTP/3 unavailable; serving HTTP/1.1 only: %v", err)
		udp = nil
	} else {
		defer udp.Close()
	}
	return serve(tcp, udp, cert, handler)
}

func serve(tcp net.Listener, udp net.PacketConn, cert tls.Certificate, handler http.Handler) error {
	_, port, err := net.SplitHostPort(tcp.Addr().String())
	if err != nil {
		return err
	}
	var h3State atomic.Int32
	if udp != nil {
		h3State.Store(h3Available)
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 1 {
			switch h3State.Load() {
			case h3Available:
				w.Header().Set("Alt-Svc", `h3=":`+port+`"; ma=3600`)
			case h3Failed:
				w.Header().Set("Alt-Svc", "clear")
			}
		}
		handler.ServeHTTP(w, r)
	})
	config := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	tcpTLS := config.Clone()
	tcpTLS.NextProtos = []string{"http/1.1"}
	h1 := &http.Server{
		Handler:           wrapped,
		TLSConfig:         tcpTLS,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	if udp == nil {
		return fmt.Errorf("HTTP/1.1 server stopped: %w", h1.Serve(tls.NewListener(tcp, tcpTLS)))
	}
	h3 := &http3.Server{
		Handler:        bodyReadDeadline(wrapped, 30*time.Second),
		TLSConfig:      http3.ConfigureTLSConfig(config.Clone()),
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	h3Errors := make(chan error, 1)
	h1Errors := make(chan error, 1)
	go func() { h3Errors <- h3.Serve(udp) }()
	go func() { h1Errors <- h1.Serve(tls.NewListener(tcp, tcpTLS)) }()
	err = awaitServers(h1Errors, h3Errors, &h3State)
	_ = h3.Close()
	_ = h1.Close()
	return fmt.Errorf("HTTP server stopped: %w", err)
}

// bodyReadDeadline limits slow HTTP/3 uploads independently of connection idle time.
func bodyReadDeadline(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(timeout)); err != nil {
				http.Error(w, "request unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// awaitServers leaves HTTP/1.1 running if HTTP/3 fails after startup.
func awaitServers(h1Errors, h3Errors <-chan error, h3State *atomic.Int32) error {
	select {
	case err := <-h1Errors:
		return err
	case err := <-h3Errors:
		h3State.Store(h3Failed)
		log.Printf("HTTP/3 stopped; serving HTTP/1.1 only: %v", err)
		return <-h1Errors
	}
}
