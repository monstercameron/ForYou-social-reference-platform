package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestCompression(t *testing.T) {
	check := func(t *testing.T, accept, want string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Header.Set("Accept-Encoding", accept)
		w := httptest.NewRecorder()
		newHandler("provider", nil).ServeHTTP(w, r)
		if got := w.Header().Get("Content-Encoding"); got != want {
			t.Fatalf("Content-Encoding = %q; want %q", got, want)
		}
		var reader io.Reader = w.Body
		if want == "gzip" {
			gz, err := gzip.NewReader(reader)
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			reader = gz
		} else if want == "br" {
			reader = brotli.NewReader(reader)
		}
		body, err := io.ReadAll(reader)
		if err != nil || !strings.Contains(string(body), `"status":"ok"`) {
			t.Errorf("decoded body = %q, error %v", body, err)
		}
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
			t.Error("missing Vary: Accept-Encoding")
		}
	}
	t.Run("brotli preferred", func(t *testing.T) { check(t, "gzip, br", "br") })
	t.Run("gzip", func(t *testing.T) { check(t, "gzip", "gzip") })
	t.Run("disabled brotli", func(t *testing.T) { check(t, "br;q=0, gzip;q=1", "gzip") })
	t.Run("identity", func(t *testing.T) { check(t, "", "") })
	t.Run("explicit identity preferred", func(t *testing.T) { check(t, "identity;q=1, gzip;q=0.5", "") })
	t.Run("wildcard chooses brotli", func(t *testing.T) { check(t, "*;q=0.5", "br") })
	t.Run("multiple header lines", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Header.Add("Accept-Encoding", "gzip;q=0")
		r.Header.Add("Accept-Encoding", "br")
		w := httptest.NewRecorder()
		newHandler("provider", nil).ServeHTTP(w, r)
		if got := w.Header().Get("Content-Encoding"); got != "br" {
			t.Errorf("Content-Encoding = %q; want br", got)
		}
	})
	t.Run("nothing acceptable", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Header.Set("Accept-Encoding", "gzip;q=0, br;q=0, identity;q=0")
		w := httptest.NewRecorder()
		newHandler("provider", nil).ServeHTTP(w, r)
		if w.Code != http.StatusNotAcceptable {
			t.Errorf("status = %d; want 406", w.Code)
		}
	})
}

func TestCompressedRangeAndComposer(t *testing.T) {
	h := testClientHandler(t)
	t.Run("video avoids redundant compression", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/static/demo-tech.mp4", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Type") != "video/mp4" || w.Body.Len() <= 16 || !strings.Contains(w.Body.String()[:16], "ftyp") {
			t.Errorf("video status %d, encoding %q, type %q, length %d", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Type"), w.Body.Len())
		}
	})
	t.Run("video compressed if identity forbidden", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/static/demo-tech.mp4", nil)
		r.Header.Set("Accept-Encoding", "gzip, identity;q=0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("video status %d, encoding %q", w.Code, w.Header().Get("Content-Encoding"))
		}
	})
	t.Run("video range remains seekable", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/static/demo-tech.mp4", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		r.Header.Set("Range", "bytes=0-9")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusPartialContent || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 10 || !strings.HasPrefix(w.Header().Get("Content-Range"), "bytes 0-9/") {
			t.Errorf("range status %d, encoding %q, content-range %q, length %d", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Range"), w.Body.Len())
		}
	})
	t.Run("range ignored when identity forbidden", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/static/demo-tech.mp4", nil)
		r.Header.Add("Accept-Encoding", "gzip")
		r.Header.Add("Accept-Encoding", "identity;q=0")
		r.Header.Set("Range", "bytes=0-9")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Range") != "" {
			t.Errorf("status %d, encoding %q, content-range %q", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Range"))
		}
	})
	t.Run("composer token is not compressed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/compose", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), `name="draft_token"`) {
			t.Errorf("status %d, encoding %q, token present %t", w.Code, w.Header().Get("Content-Encoding"), strings.Contains(w.Body.String(), `name="draft_token"`))
		}
	})
	t.Run("quote page token is not compressed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/quote?text=hello", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), `name="draft_token"`) {
			t.Errorf("status %d, encoding %q, token present %t", w.Code, w.Header().Get("Content-Encoding"), strings.Contains(w.Body.String(), `name="draft_token"`))
		}
	})
}

func TestSecretPagesAvoidCompression(t *testing.T) {
	client := newTestClient(t, false)
	t.Run("identity", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/identity", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" {
			t.Errorf("identity response = %d with encoding %q", w.Code, w.Header().Get("Content-Encoding"))
		}
	})
	t.Run("wallet", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/wallet", nil)
		r.Header.Set("Accept-Encoding", "gzip, br")
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "" {
			t.Errorf("wallet response = %d with encoding %q", w.Code, w.Header().Get("Content-Encoding"))
		}
	})
}

func TestCompressedResponseHeaders(t *testing.T) {
	t.Run("implicit status clears uncompressed length", func(t *testing.T) {
		h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "11")
			_, _ = io.WriteString(w, "hello world")
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Content-Length") != "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("content length %q, content type %q", w.Header().Get("Content-Length"), w.Header().Get("Content-Type"))
		}
		reader, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		if err != nil || string(body) != "hello world" {
			t.Errorf("decoded body %q, error %v", body, err)
		}
	})
	t.Run("not modified has no compressed body", func(t *testing.T) {
		h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("status %d, body length %d", w.Code, w.Body.Len())
		}
	})
}
