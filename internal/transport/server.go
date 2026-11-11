// Package transport serves handlers over HTTP/3 and HTTP/1.1, or plain HTTP in dev mode.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
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

// Config selects how one role listener serves.
type Config struct {
	Addr     string
	CertFile string
	KeyFile  string
	Dev      bool
}

var (
	// ErrTLSRequired indicates TLS credentials are required outside dev mode.
	ErrTLSRequired = errors.New("set FORYOU_TLS_CERT and FORYOU_TLS_KEY")
	// ErrIncompleteTLS indicates exactly one TLS credential was configured.
	ErrIncompleteTLS = errors.New("FORYOU_TLS_CERT and FORYOU_TLS_KEY must be set together")
	// ErrPlainHTTPNotLoopback indicates plain HTTP was configured outside loopback.
	ErrPlainHTTPNotLoopback = errors.New("plain HTTP is limited to loopback addresses")
)

// ShutdownTimeout bounds graceful HTTP shutdown.
const ShutdownTimeout = 5 * time.Second

// Listener owns the sockets selected by Listen.
type Listener struct {
	tcp  net.Listener
	udp  net.PacketConn
	cert tls.Certificate
	tls  bool
	url  string
}

// IsLoopbackHost reports whether host is exactly localhost or a loopback IP literal.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listen binds the configured address before returning.
func Listen(cfg Config) (*Listener, error) {
	hasCert, hasKey := cfg.CertFile != "", cfg.KeyFile != ""
	if hasCert != hasKey {
		return nil, ErrIncompleteTLS
	}
	if !cfg.Dev && !hasCert {
		return nil, ErrTLSRequired
	}
	if cfg.Dev {
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || !IsLoopbackHost(host) {
			return nil, fmt.Errorf("%w: %q", ErrPlainHTTPNotLoopback, cfg.Addr)
		}
	}
	var cert tls.Certificate
	var err error
	if hasCert && !cfg.Dev {
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate: %w", err)
		}
	}
	tcp, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("listen TCP: %w", err)
	}
	l := &Listener{tcp: tcp, cert: cert, tls: hasCert && !cfg.Dev}
	if l.tls {
		l.udp, err = net.ListenPacket("udp", tcp.Addr().String())
		if err != nil {
			log.Printf("HTTP/3 unavailable; serving HTTP/1.1 only: %v", err)
		}
	} else {
		boundHost, _, boundErr := net.SplitHostPort(tcp.Addr().String())
		boundIP := net.ParseIP(boundHost)
		if boundErr != nil || boundIP == nil || !boundIP.IsLoopback() {
			_ = tcp.Close()
			return nil, fmt.Errorf("%w: %q", ErrPlainHTTPNotLoopback, cfg.Addr)
		}
	}
	boundHost, port, err := net.SplitHostPort(tcp.Addr().String())
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	scheme := "http"
	if l.tls {
		scheme = "https"
	}
	l.url = scheme + "://" + net.JoinHostPort(boundHost, port)
	return l, nil
}

// Addr returns the bound TCP address.
func (l *Listener) Addr() net.Addr { return l.tcp.Addr() }

// URL returns the listener's base URL, including its bound port.
func (l *Listener) URL() string { return l.url }

// TLS reports whether the listener uses TLS.
func (l *Listener) TLS() bool { return l.tls }

// Close closes the listener sockets.
func (l *Listener) Close() error {
	var tcpErr, udpErr error
	if l.tcp != nil {
		tcpErr = l.tcp.Close()
	}
	if l.udp != nil {
		udpErr = l.udp.Close()
	}
	return errors.Join(tcpErr, udpErr)
}

// Serve runs the listener until it fails or ctx is cancelled.
func (l *Listener) Serve(ctx context.Context, handler http.Handler) error {
	if !l.tls {
		return servePlain(ctx, l.tcp, handler)
	}
	return serve(ctx, l.tcp, l.udp, l.cert, handler)
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
}

func servePlain(ctx context.Context, listener net.Listener, handler http.Handler) error {
	h1 := newHTTPServer(handler)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- h1.Serve(listener) }()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server stopped: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer cancel()
		shutdownErr := h1.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = h1.Close()
		}
		serveErr := <-serveErrors
		if shutdownErr != nil {
			return shutdownErr
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

func serve(ctx context.Context, tcp net.Listener, udp net.PacketConn, cert tls.Certificate, handler http.Handler) error {
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
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	tcpTLS := tlsConfig.Clone()
	tcpTLS.NextProtos = []string{"http/1.1"}
	h1 := newHTTPServer(wrapped)
	h1.TLSConfig = tcpTLS
	h1Errors := make(chan error, 1)
	go func() { h1Errors <- h1.Serve(tls.NewListener(tcp, tcpTLS)) }()
	var h3 *http3.Server
	h3Errors := make(chan error, 1)
	if udp != nil {
		h3 = &http3.Server{Handler: bodyReadDeadline(wrapped, 30*time.Second), TLSConfig: http3.ConfigureTLSConfig(tlsConfig.Clone()), IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
		go func() { h3Errors <- h3.Serve(udp) }()
	}
	var serveErr error
	h3Stopped := h3 == nil
	select {
	case serveErr = <-h1Errors:
	case err = <-h3Errors:
		h3Stopped = true
		h3State.Store(h3Failed)
		log.Printf("HTTP/3 stopped; serving HTTP/1.1 only: %v", err)
		select {
		case serveErr = <-h1Errors:
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	var h3CloseErr error
	if h3 != nil {
		h3CloseErr = h3.Close()
		if !h3Stopped {
			select {
			case <-h3Errors:
			case <-shutdownCtx.Done():
				if h3CloseErr == nil {
					h3CloseErr = shutdownCtx.Err()
				}
			}
		}
	}
	shutdownErr := h1.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = h1.Close()
	}
	if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
		select {
		case serveErr = <-h1Errors:
		default:
		}
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if h3CloseErr != nil {
		return h3CloseErr
	}
	if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("HTTP server stopped: %w", serveErr)
}

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
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		log.Printf("HTTP/3 unavailable; serving HTTP/1.1 only: %v", err)
	} else {
		defer udp.Close()
	}
	return serve(context.Background(), tcp, udp, cert, handler)
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
