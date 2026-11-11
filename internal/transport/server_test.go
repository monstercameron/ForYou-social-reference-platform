package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestHTTP3AndHTTP11(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), tcp, udp, cert, transportTestHandler()) }()
	defer func() {
		_ = tcp.Close()
		_ = udp.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("transport did not stop after its listeners closed")
		}
	}()
	origin := "https://" + tcp.Addr().String()
	check := func(t *testing.T, client *http.Client, major int) {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client.Jar = jar
		response, err := client.Get(origin + "/thread/fragment?id=garden&page=2")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.ProtoMajor != major || response.StatusCode != http.StatusOK {
			t.Errorf("response = %s %d; want HTTP/%d 200", response.Proto, response.StatusCode, major)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil || !strings.Contains(string(body), "Tomatoes and basil") {
			t.Errorf("thread response missing expected reply; read error %v", err)
		}
		if major == 1 && !strings.Contains(response.Header.Get("Alt-Svc"), "h3=") {
			t.Error("HTTP/1.1 response does not advertise HTTP/3")
		}
		feedback, err := client.PostForm(origin+"/feedback/fragment", url.Values{"id": {"maya"}, "vote": {"up"}})
		if err != nil {
			t.Fatal(err)
		}
		feedbackBody, err := io.ReadAll(feedback.Body)
		_ = feedback.Body.Close()
		if err != nil || feedback.ProtoMajor != major || feedback.StatusCode != http.StatusOK || !strings.Contains(string(feedbackBody), `aria-label="Positive feedback" aria-pressed="true"`) {
			t.Fatalf("feedback response: protocol %s, status %d, read error %v", feedback.Proto, feedback.StatusCode, err)
		}
		secondFeedback, err := client.PostForm(origin+"/feedback/fragment", url.Values{"id": {"elliot"}, "vote": {"down"}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, secondFeedback.Body)
		_ = secondFeedback.Body.Close()
		if err != nil || secondFeedback.ProtoMajor != major || secondFeedback.StatusCode != http.StatusOK {
			t.Fatalf("second feedback: protocol %s, status %d, read error %v", secondFeedback.Proto, secondFeedback.StatusCode, err)
		}
		explore, err := client.Get(origin + "/explore")
		if err != nil {
			t.Fatal(err)
		}
		defer explore.Body.Close()
		exploreBody, err := io.ReadAll(explore.Body)
		if err != nil || explore.ProtoMajor != major || explore.StatusCode != http.StatusOK || !strings.Contains(string(exploreBody), `aria-label="Positive feedback" aria-pressed="true"`) || !strings.Contains(string(exploreBody), `aria-label="Negative feedback" aria-pressed="true"`) {
			t.Errorf("Explore response: protocol %s, status %d, read error %v", explore.Proto, explore.StatusCode, err)
		}
		r, err := http.NewRequest(http.MethodGet, origin+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Large", strings.Repeat("x", 80<<10))
		tooLarge, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer tooLarge.Body.Close()
		if tooLarge.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Errorf("HTTP/%d oversized header status %d; want 431", major, tooLarge.StatusCode)
		}
	}
	t.Run("HTTP3", func(t *testing.T) {
		tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
		defer tr.Close()
		check(t, &http.Client{Transport: tr, Timeout: 5 * time.Second}, 3)
	})
	t.Run("HTTP11", func(t *testing.T) {
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}}}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
		check(t, client, 1)
	})
}

func TestHTTP11WhenUDPPortIsOccupied(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	occupied, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), tcp, nil, cert, transportTestHandler()) }()
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	response, err := client.Get("https://" + tcp.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.ProtoMajor != 1 || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) || response.Header.Get("Alt-Svc") != "" {
		t.Errorf("fallback response: protocol %s, status %d, Alt-Svc %q, read error %v", response.Proto, response.StatusCode, response.Header.Get("Alt-Svc"), err)
	}
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Error("expected server to stop after closing the TCP listener")
	}
}

func TestHTTP3StalledBodyDeadline(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	readErrors := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErrors <- err
		if err != nil {
			http.Error(w, "request timed out", http.StatusRequestTimeout)
		}
	})
	h3 := &http3.Server{TLSConfig: http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}}), Handler: bodyReadDeadline(handler, 200*time.Millisecond)}
	served := make(chan error, 1)
	go func() { served <- h3.Serve(udp) }()
	defer func() {
		_ = h3.Close()
		<-served
	}()
	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer tr.Close()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	r, err := http.NewRequest(http.MethodPost, "https://"+udp.LocalAddr().String()+"/", reader)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		response, err := client.Do(r)
		if response != nil {
			_ = response.Body.Close()
		}
		result <- err
	}()
	written := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("partial")); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write partial body: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("partial body did not reach server")
	}
	select {
	case err := <-readErrors:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("body read error = %v; want deadline exceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stalled request body was not stopped")
	}
	_ = writer.Close()
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Error("client did not finish after deadline")
	}
}

type failureLog struct {
	once sync.Once
	done chan struct{}
}

func (w *failureLog) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "HTTP/3 stopped; serving HTTP/1.1 only") {
		w.once.Do(func() { close(w.done) })
	}
	return len(p), nil
}

func TestHTTP11AfterUDPFailure(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	waiter := &failureLog{done: make(chan struct{})}
	previousWriter := log.Writer()
	log.SetOutput(waiter)
	defer log.SetOutput(previousWriter)
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), tcp, udp, cert, transportTestHandler()) }()
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	url := "https://" + tcp.Addr().String() + "/healthz"
	before, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, before.Body)
	_ = before.Body.Close()
	if !strings.Contains(before.Header.Get("Alt-Svc"), "h3=") {
		t.Fatal("initial response did not advertise HTTP/3")
	}
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiter.done:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP/3 failure was not detected")
	}
	after, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Body.Close()
	if after.StatusCode != http.StatusOK || after.ProtoMajor != 1 || after.Header.Get("Alt-Svc") != "clear" {
		t.Errorf("fallback response: protocol %s, status %d, Alt-Svc %q", after.Proto, after.StatusCode, after.Header.Get("Alt-Svc"))
	}
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Error("expected server to stop after closing the TCP listener")
	}
}

func transportTestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thread/fragment", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "Tomatoes and basil") })
	mux.HandleFunc("POST /feedback/fragment", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		vote := r.Form.Get("vote")
		if vote != "up" && vote != "down" {
			http.Error(w, "bad vote", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "feedback_" + r.Form.Get("id"), Value: vote, Path: "/"})
		_, _ = io.WriteString(w, `aria-label="Positive feedback" aria-pressed="true"`)
	})
	mux.HandleFunc("GET /explore", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("feedback_maya"); err == nil && cookie.Value == "up" {
			_, _ = io.WriteString(w, `aria-label="Positive feedback" aria-pressed="true"`)
		}
		if cookie, err := r.Cookie("feedback_elliot"); err == nil && cookie.Value == "down" {
			_, _ = io.WriteString(w, `aria-label="Negative feedback" aria-pressed="true"`)
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	return mux
}
