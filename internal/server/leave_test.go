package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

// mdRecords serves fixed posts from the record source, counting lookups.
type mdRecords struct {
	recordSource
	posts map[string]post
	asked *[]string
}

func (s mdRecords) Get(_ context.Context, _ *http.Request, id string) (post, Coverage, bool, error) {
	*s.asked = append(*s.asked, id)
	p, ok := s.posts[id]
	return p, Coverage{Note: "Test records"}, ok, nil
}

func mdTestHandler(t *testing.T, asked *[]string, posts map[string]post) http.Handler {
	t.Helper()
	client := newTestClient(t, false)
	client.app.recordSource = mdRecords{recordSource: client.app.recordSource, posts: posts, asked: asked}
	return client.Handler()
}

func mdGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestLeaveConfirmation(t *testing.T) {
	var asked []string
	refs := []wire.PostReference{{ID: "r1", Target: "did:key:z6MkExampleAuthor"}, {ID: "r2", Target: "fy1:target"}}
	posts := map[string]post{
		"fy1:p1":     {ID: "fy1:p1", Format: "text/markdown", Text: "[https://good.example](https://evil.example:8443/x) and [docs](https://example.com/a) and [b](https://xn--bcher-kva.example/)", Payload: wire.PostPayload{Refs: &refs}},
		"fy1:target": {ID: "fy1:target", Format: "text/plain", Text: "The target post"},
		"fy1:plain":  {ID: "fy1:plain", Format: "text/plain", Text: "No links here"},
	}
	h := mdTestHandler(t, &asked, posts)
	t.Run("mismatch warning, host, port and exact destination", func(t *testing.T) {
		w := mdGet(h, "/leave?post=fy1:p1&link=0")
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Content-Security-Policy") != "default-src 'self'" {
			t.Fatalf("status %d, referrer %q, csp %q", w.Code, w.Header().Get("Referrer-Policy"), w.Header().Get("Content-Security-Policy"))
		}
		if !strings.Contains(body, "The link text looks like a different address than where it goes.") {
			t.Error("confirmation page is missing the mismatch warning")
		}
		if !strings.Contains(body, "Host: evil.example") {
			t.Error("confirmation page is missing the host")
		}
		if !strings.Contains(body, "Port: 8443") {
			t.Error("confirmation page is missing the port")
		}
		if !strings.Contains(body, "https://evil.example:8443/x") {
			t.Error("confirmation page is missing the exact destination")
		}
		if !strings.Contains(body, `rel="noopener noreferrer"`) {
			t.Error("confirmation page is missing rel=noopener noreferrer")
		}
		if !strings.Contains(body, `referrerpolicy="no-referrer"`) {
			t.Error("confirmation page is missing referrerpolicy=no-referrer")
		}
		if strings.Contains(body, "ping=") || strings.Contains(body, "preconnect") || strings.Contains(body, "dns-prefetch") {
			t.Error("confirmation page allows prefetch-like behaviour")
		}
	})
	t.Run("no mismatch warning when the label matches the host", func(t *testing.T) {
		if body := mdGet(h, "/leave?post=fy1:p1&link=1").Body.String(); strings.Contains(body, "looks like a different address") || !strings.Contains(body, "Host: example.com") {
			t.Error("plain label should not show the mismatch warning")
		}
	})
	t.Run("IDN host is shown as written", func(t *testing.T) {
		body := mdGet(h, "/leave?post=fy1:p1&link=2").Body.String()
		if !strings.Contains(body, "Host: xn--bcher-kva.example") || strings.Contains(body, "bücher") {
			t.Error("IDN host was not shown in its A-label form")
		}
	})
	t.Run("link index out of range is not found", func(t *testing.T) {
		if w := mdGet(h, "/leave?post=fy1:p1&link=5"); w.Code != http.StatusNotFound {
			t.Errorf("status %d; want 404", w.Code)
		}
	})
	t.Run("post without links is not found", func(t *testing.T) {
		if w := mdGet(h, "/leave?post=fy1:plain&link=0"); w.Code != http.StatusNotFound {
			t.Errorf("status %d; want 404", w.Code)
		}
	})
	t.Run("unknown post is not found", func(t *testing.T) {
		if w := mdGet(h, "/leave?post=fy1:missing&link=0"); w.Code != http.StatusNotFound {
			t.Errorf("status %d; want 404", w.Code)
		}
	})
}

func TestReferenceResolution(t *testing.T) {
	var asked []string
	refs := []wire.PostReference{{ID: "r1", Target: "did:key:z6MkExampleAuthor"}, {ID: "r2", Target: "fy1:target"}, {ID: "r3", Target: "fy1:absent"}}
	posts := map[string]post{
		"fy1:p1":     {ID: "fy1:p1", Format: "text/markdown", Text: "[one](fy-ref:r1) [two](fy-ref:r2) [three](fy-ref:r3)", Payload: wire.PostPayload{Refs: &refs}},
		"fy1:target": {ID: "fy1:target", Format: "text/plain", Text: "The target post"},
	}
	h := mdTestHandler(t, &asked, posts)
	t.Run("author reference shows the key without a lookup", func(t *testing.T) {
		asked = nil
		w := mdGet(h, "/ref?post=fy1:p1&ref=r1")
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "did:key:z6MkExampleAuthor") || !strings.Contains(body, `href="/explore?author=did%3Akey%3Az6MkExampleAuthor"`) {
			t.Errorf("author reference status %d; body missing key or explore link", w.Code)
		}
		if len(asked) != 1 || asked[0] != "fy1:p1" {
			t.Errorf("author reference asked the sources for %v; want only the post itself", asked)
		}
	})
	t.Run("message reference shows the verified target", func(t *testing.T) {
		w := mdGet(h, "/ref?post=fy1:p1&ref=r2")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "The target post") || !strings.Contains(w.Body.String(), "Resolving a reference tells the providers you selected that you looked it up.") {
			t.Errorf("message reference status %d", w.Code)
		}
	})
	t.Run("absent message target is not available, never does not exist", func(t *testing.T) {
		body := mdGet(h, "/ref?post=fy1:p1&ref=r3").Body.String()
		if !strings.Contains(body, "Not available from your sources.") || strings.Contains(body, "does not exist") {
			t.Error("absent target was not reported as unavailable from sources")
		}
	})
	t.Run("undeclared reference is not found", func(t *testing.T) {
		if w := mdGet(h, "/ref?post=fy1:p1&ref=nope"); w.Code != http.StatusNotFound {
			t.Errorf("status %d; want 404", w.Code)
		}
	})
	t.Run("absent message target still shows the canonical target", func(t *testing.T) {
		if body := mdGet(h, "/ref?post=fy1:p1&ref=r3").Body.String(); !strings.Contains(body, `Target: <code>fy1:absent</code>`) {
			t.Error("reference page does not show the canonical target")
		}
	})
	t.Run("closing puts the open button back without a target lookup", func(t *testing.T) {
		asked = nil
		w := mdGet(h, "/ref/fragment?post=fy1:p1&ref=r2&closed=1")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<button type="submit" class="btn-secondary md-ref-open">Show reference: <span class="md-target">fy1:target</span></button>`) {
			t.Errorf("close fragment status %d body %q", w.Code, w.Body.String())
		}
		if len(asked) != 1 || asked[0] != "fy1:p1" {
			t.Errorf("closing asked the sources for %v; want only the post itself", asked)
		}
	})
	t.Run("fragment fills its region", func(t *testing.T) {
		body := mdGet(h, "/ref/fragment?post=fy1:p1&ref=r2").Body.String()
		if !strings.Contains(body, "The target post") {
			t.Error("fragment did not show the resolved target")
		}
		if !strings.Contains(body, `<h2 class="md-ref-title md-ref-focus" tabindex="-1">Reference</h2>`) || !strings.Contains(body, `<form class="md-ref-close" action="/posts/fy1:p1"`) {
			t.Errorf("fragment has no focus target or close form: %q", body)
		}
	})
}
