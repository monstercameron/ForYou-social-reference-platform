package server

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

const (
	medTestPost     = "fy1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	medTestFigure   = "media-0123456789ab-a1"
	medTestProfile  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	medOtherProfile = "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	medTestPNG      = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	medTestPNGSHA   = "63ef318d96b5d0d0ceba6e04a4e622b1158335cdc67c49e27839132c6f655058"
	medTestPNGSize  = 68
	medTestSVG      = `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`
	medTestSVGSHA   = "7a108193a3c54e5c07c1207c0e38b0279d9cddc0fb844fdbdcaf82629ec69eb8"
	medTestSVGSize  = 67
)

type medTestResolver struct{}

func (medTestResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// medTestPNGBytes returns the 1x1 PNG whose digest and size the fixtures declare.
func medTestPNGBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(medTestPNG)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// medTestServer answers HTTPS requests on 127.0.0.1 and counts them.
func medTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, hits
}

// medTestPort returns the port a fake server listens on.
func medTestPort(t *testing.T, server *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// medUseFake points the isolated client at a fake server reached through the name example.com.
func medUseFake(t *testing.T, server *httptest.Server) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	previous := medFetchOverride
	medFetchOverride = func(config *content.FetchConfig) {
		config.Policy = content.DestinationPolicy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, Resolver: medTestResolver{}}
		config.RootCAs = pool
	}
	t.Cleanup(func() { medFetchOverride = previous })
}

// medUseRecord makes the post visible to the media routes without a provider.
func medUseRecord(t *testing.T, p post) {
	t.Helper()
	previous := medRecord
	medRecord = func(_ *App, _ *http.Request, id string) (post, bool, error) {
		if id == p.ID {
			return p, true, nil
		}
		return post{}, false, nil
	}
	t.Cleanup(func() { medRecord = previous })
}

func medTestSlotAt(rawURL, sha string, size int64) wire.MediaReference {
	alt := "A small square"
	return wire.MediaReference{ID: "a1", URL: rawURL, Kind: "image", SHA256: sha, SizeBytes: size, AltText: &alt}
}

func medTestPostWith(slots ...wire.MediaReference) post {
	return post{ID: medTestPost, Slots: slots}
}

func medTestCookie(profile string) *http.Cookie {
	return &http.Cookie{Name: profileCookie, Value: profile}
}

func medConsent(t *testing.T, app *App, profile string, origins ...string) {
	t.Helper()
	if err := app.updateSettings(profile, func(s *ProfileSettings) error {
		s.MediaOrigins = origins
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// medPost posts a load form, as HTMLeX (fragment) or as a plain form.
func medPost(h http.Handler, form url.Values, htmlex bool, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return medPostTo(h, "/media/load", form, htmlex, cookies...)
}

// medPostTo posts a form to a media route, as HTMLeX (fragment) or as a plain form.
func medPostTo(h http.Handler, path string, form url.Values, htmlex bool, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmlex {
		r.Header.Set("Sec-Fetch-Dest", "empty")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func medGet(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMediaSlotsRender(t *testing.T) {
	client := newTestClient(t, false)
	app := client.app
	p := medTestPostWith(medTestSlotAt("https://example.com/a.png", medTestPNGSHA, medTestPNGSize))

	t.Run("default render loads nothing and names the origin to consent to", func(t *testing.T) {
		html := string(renderPostMedia(app, "", ProfileSettings{}, p, time.Unix(0, 0)))
		if strings.Contains(html, "<img") || strings.Contains(html, "<video") || strings.Contains(html, "/a.png") {
			t.Fatalf("unconsented slot rendered media or its address: %s", html)
		}
		if !strings.Contains(html, "Load from example.com") {
			t.Fatalf("unconsented slot lacks its load control: %s", html)
		}
	})

	t.Run("a consented origin waits for its own lazy load", func(t *testing.T) {
		html := string(renderPostMedia(app, medTestProfile, ProfileSettings{MediaOrigins: []string{"https://example.com:443"}}, p, time.Unix(0, 0)))
		if !strings.Contains(html, `auto="lazy"`) || strings.Contains(html, "<img") {
			t.Fatalf("consented slot without bytes is not a lazy load: %s", html)
		}
	})

	t.Run("a verified object for a consented origin shows from this server", func(t *testing.T) {
		medStateFor(app).cache.put(medKey{sha: medTestPNGSHA, size: medTestPNGSize}, content.VerifiedMedia{Bytes: medTestPNGBytes(t), Type: content.MediaPNG, Width: 1, Height: 1})
		html := string(renderPostMedia(app, medTestProfile, ProfileSettings{MediaOrigins: []string{"https://example.com:443"}}, p, time.Unix(0, 0)))
		if !strings.Contains(html, `src="/media/`+medTestPost+`/a1"`) || !strings.Contains(html, `width="1" height="1"`) {
			t.Fatalf("verified object is not shown from this server: %s", html)
		}
	})
}

func TestMediaFetch(t *testing.T) {
	ctx := context.Background()
	allowAll := func(string) bool { return true }

	t.Run("verified bytes come back with their sniffed type", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		slot := medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)
		media, err := medFetch(ctx, slot, allowAll)
		if err != nil || media.Type != content.MediaPNG || media.Width != 1 || len(media.Bytes) != medTestPNGSize {
			t.Fatalf("medFetch = %v, %v; want the verified 68-byte PNG", media.Type, err)
		}
	})

	t.Run("bytes that differ from the signed digest are refused", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(medTestSVG))
		})
		medUseFake(t, server)
		slot := medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, 0)
		if _, err := medFetch(ctx, slot, allowAll); !errors.Is(err, content.ErrDigestMismatch) {
			t.Fatalf("medFetch error = %v; want digest mismatch", err)
		}
	})

	t.Run("a body larger than the declared size is refused", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		slot := medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, 10)
		if _, err := medFetch(ctx, slot, allowAll); !errors.Is(err, content.ErrMediaTooLarge) {
			t.Fatalf("medFetch error = %v; want too large", err)
		}
	})

	t.Run("a compressed response is refused", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		slot := medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)
		if _, err := medFetch(ctx, slot, allowAll); !errors.Is(err, content.ErrEncodedResponse) {
			t.Fatalf("medFetch error = %v; want encoded response", err)
		}
	})

	t.Run("an SVG with the matching digest is unsupported", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(medTestSVG))
		})
		medUseFake(t, server)
		slot := medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.svg", medTestSVGSHA, medTestSVGSize)
		if _, err := medFetch(ctx, slot, allowAll); !errors.Is(err, content.ErrUnsupportedMedia) {
			t.Fatalf("medFetch error = %v; want unsupported media", err)
		}
	})

	t.Run("a loopback address is blocked by the production policy and never contacted", func(t *testing.T) {
		server, hits := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(medTestPNGBytes(t))
		})
		slot := medTestSlotAt("https://localhost:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)
		if _, err := medFetch(ctx, slot, allowAll); !errors.Is(err, content.ErrBlockedDestination) {
			t.Fatalf("medFetch error = %v; want blocked destination", err)
		}
		if hits.Load() != 0 {
			t.Fatalf("blocked destination received %d requests; want none", hits.Load())
		}
	})

	t.Run("failures are shown as their own state in words", func(t *testing.T) {
		slot := medTestSlotAt("https://example.com/a.png", medTestPNGSHA, medTestPNGSize)
		now := time.Unix(0, 0)
		if got := medFailureOf(content.ErrDigestMismatch, "", slot, now).state; got != medMismatch {
			t.Fatalf("digest mismatch state = %q; want %q", got, medMismatch)
		}
		moved := medFailureOf(fmt.Errorf("wrapped: %w", content.ErrRedirectRefused), "https://other.example:443", slot, now)
		if moved.state != medRedirect || moved.moved != "https://other.example:443" {
			t.Fatalf("redirect state = %+v; want redirect to other.example", moved)
		}
	})

	t.Run("a declared size over the client budget is named as that", func(t *testing.T) {
		big := medTestSlotAt("https://example.com/a.png", medTestPNGSHA, 33<<20)
		failure := medFailureOf(content.ErrMediaTooLarge, "", big, time.Unix(0, 0))
		if failure.state != medTooLarge || failure.text != "Larger than this client loads (32 MB). Not shown." {
			t.Fatalf("failure = %+v; want the client budget named", failure)
		}
	})

	t.Run("a shared flight that followed a refused redirect is not shared with a reader who approves it", func(t *testing.T) {
		flight := &medFlight{moved: "https://example.com:8443"}
		approves := func(origin string) bool { return origin == "https://example.com:8443" }
		if flight.usableBy(approves) {
			t.Fatal("a refused redirect was shared with a reader who approves it")
		}
	})

	t.Run("a shared flight that followed an approved redirect is not shared with a reader who refuses it", func(t *testing.T) {
		flight := &medFlight{touched: []string{"https://example.com:8443"}}
		refuses := func(string) bool { return false }
		if flight.usableBy(refuses) {
			t.Fatal("a redirect the starting reader approved was shared with a reader who refuses it")
		}
	})
}

func TestMediaCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	server, _ := medTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	})
	medUseFake(t, server)
	origin := "https://example.com:" + medTestPort(t, server)
	slot := medTestSlotAt(origin+"/a.png", medTestPNGSHA, medTestPNGSize)
	fetcher := newMedFetcher(newMedCache())

	t.Run("cancelling an origin stops the fetch that reaches it", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			_, _, err := fetcher.load(context.Background(), slot, func(string) bool { return true })
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the fetch never reached the server")
		}
		fetcher.cancelOrigin(origin)
		select {
		case <-cancelled:
		case <-time.After(10 * time.Second):
			t.Fatal("the fetch kept running after its origin was cancelled")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("load error = %v; want canceled", err)
		}
	})
}

func TestMediaObjectRoute(t *testing.T) {
	client := newTestClient(t, false)
	app := client.app
	handler := client.Handler()
	p := medTestPostWith(medTestSlotAt("https://example.com/a.png", medTestPNGSHA, medTestPNGSize))
	medUseRecord(t, p)
	medStateFor(app).cache.put(medKey{sha: medTestPNGSHA, size: medTestPNGSize}, content.VerifiedMedia{Bytes: medTestPNGBytes(t), Type: content.MediaPNG, Width: 1, Height: 1})
	medConsent(t, app, medTestProfile, "https://example.com:443")
	path := "/media/" + medTestPost + "/a1"

	t.Run("a consenting reader gets the verified bytes with protective headers", func(t *testing.T) {
		w := medGet(handler, path, medTestCookie(medTestProfile))
		if w.Code != http.StatusOK || w.Body.Len() != medTestPNGSize {
			t.Fatalf("status %d with %d bytes; want 200 with the 68-byte PNG", w.Code, w.Body.Len())
		}
		if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Fatalf("protective headers missing: %v", w.Header())
		}
	})

	t.Run("a reader without consent gets not found", func(t *testing.T) {
		if w := medGet(handler, path, medTestCookie(medOtherProfile)); w.Code != http.StatusNotFound {
			t.Fatalf("status %d for a reader without consent; want 404", w.Code)
		}
	})

	t.Run("revoked consent gets not found", func(t *testing.T) {
		medConsent(t, app, medTestProfile)
		if w := medGet(handler, path, medTestCookie(medTestProfile)); w.Code != http.StatusNotFound {
			t.Fatalf("status %d after revoking consent; want 404", w.Code)
		}
	})
}

func TestMediaLoadRoute(t *testing.T) {
	// medLoadFixture serves the PNG from a fake origin and makes the post visible.
	medLoadFixture := func(t *testing.T) (*App, http.Handler, string) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		client := newTestClient(t, false)
		medUseRecord(t, medTestPostWith(medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)))
		return client.app, client.Handler(), medTestPort(t, server)
	}

	t.Run("a plain form load returns the post with the item verified", func(t *testing.T) {
		app, handler, _ := medLoadFixture(t)
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}}
		w := medPost(handler, form, false, medTestCookie(medTestProfile))
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "#"+medTestFigure) {
			t.Fatalf("status %d location %q; want 303 to the item", w.Code, w.Header().Get("Location"))
		}
		if _, ok := medStateFor(app).cache.get(medKey{sha: medTestPNGSHA, size: medTestPNGSize}); !ok {
			t.Fatal("loaded object is not held after the click")
		}
	})

	t.Run("a click on an object already held contacts nothing", func(t *testing.T) {
		server, hits := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		client := newTestClient(t, false)
		medUseRecord(t, medTestPostWith(medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)))
		medStateFor(client.app).cache.put(medKey{sha: medTestPNGSHA, size: medTestPNGSize}, content.VerifiedMedia{Bytes: medTestPNGBytes(t), Type: content.MediaPNG, Width: 1, Height: 1})
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}}
		w := medPost(client.Handler(), form, true, medTestCookie(medOtherProfile))
		if !strings.Contains(w.Body.String(), `src="/media/`+medTestPost+`/a1"`) || hits.Load() != 0 {
			t.Fatalf("held object: body %q with %d upstream requests; want the verified image and none", w.Body.String(), hits.Load())
		}
	})

	t.Run("an HTMLeX load answers with the figure alone", func(t *testing.T) {
		_, handler, _ := medLoadFixture(t)
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}}
		w := medPost(handler, form, true, medTestCookie(medTestProfile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<figure id="`+medTestFigure+`"`) || !strings.Contains(body, `<img class="media-image"`) || strings.Contains(body, "<html") {
			t.Fatalf("status %d; figure fragment %q", w.Code, body)
		}
	})

	t.Run("always load stores the origin so the next render is loaded", func(t *testing.T) {
		app, handler, port := medLoadFixture(t)
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}, "always": {"1"}}
		if w := medPost(handler, form, true, medTestCookie(medTestProfile)); w.Code != http.StatusOK {
			t.Fatalf("status %d; want 200", w.Code)
		}
		settings, err := app.settings(medTestProfile)
		if err != nil || len(settings.MediaOrigins) != 1 || settings.MediaOrigins[0] != "https://example.com:"+port {
			t.Fatalf("stored origins = %v, %v; want the slot origin", settings.MediaOrigins, err)
		}
	})

	t.Run("a redirect to another origin waits for the reader's approval, then loads from there", func(t *testing.T) {
		target, targetHits := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		source, _ := medTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com:"+medTestPort(t, target)+"/b.png", http.StatusFound)
		})
		medUseFake(t, source)
		client := newTestClient(t, false)
		medUseRecord(t, medTestPostWith(medTestSlotAt("https://example.com:"+medTestPort(t, source)+"/a.png", medTestPNGSHA, medTestPNGSize)))
		handler := client.Handler()
		moved := "https://example.com:" + medTestPort(t, target)
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}}
		w := medPost(handler, form, true, medTestCookie(medTestProfile))
		if !strings.Contains(w.Body.String(), "This media moved to "+moved) || targetHits.Load() != 0 {
			t.Fatalf("unapproved redirect: body %q with %d requests to the target; want the moved notice and none", w.Body.String(), targetHits.Load())
		}
		form.Set("approve", moved)
		w = medPost(handler, form, true, medTestCookie(medTestProfile))
		if !strings.Contains(w.Body.String(), `src="/media/`+medTestPost+`/a1"`) || targetHits.Load() != 1 {
			t.Fatalf("approved redirect: body %q with %d requests to the target; want the verified image and one request", w.Body.String(), targetHits.Load())
		}
	})

	t.Run("bytes that differ from the signed digest are not shown and not served", func(t *testing.T) {
		server, _ := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(medTestSVG))
		})
		medUseFake(t, server)
		client := newTestClient(t, false)
		medUseRecord(t, medTestPostWith(medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, 0)))
		handler := client.Handler()
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}}
		w := medPost(handler, form, true, medTestCookie(medTestProfile))
		if !strings.Contains(w.Body.String(), "This file differs from the one the author signed. Not shown.") {
			t.Fatalf("mismatch not shown: %q", w.Body.String())
		}
		if got := medGet(handler, "/media/"+medTestPost+"/a1", medTestCookie(medTestProfile)); got.Code != http.StatusNotFound {
			t.Fatalf("status %d for the mismatched object; want 404", got.Code)
		}
	})

	t.Run("a lazy load from a page rendered before consent was revoked contacts nothing", func(t *testing.T) {
		server, hits := medTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(medTestPNGBytes(t))
		})
		medUseFake(t, server)
		client := newTestClient(t, false)
		medUseRecord(t, medTestPostWith(medTestSlotAt("https://example.com:"+medTestPort(t, server)+"/a.png", medTestPNGSHA, medTestPNGSize)))
		handler := client.Handler()
		form := url.Values{"post": {medTestPost}, "slot": {"a1"}, "auto": {"1"}}
		w := medPost(handler, form, true, medTestCookie(medTestProfile))
		if hits.Load() != 0 || strings.Contains(w.Body.String(), "<img") {
			t.Fatalf("stale lazy load made %d requests with body %q; want none", hits.Load(), w.Body.String())
		}
		if got := medGet(handler, "/media/"+medTestPost+"/a1", medTestCookie(medTestProfile)); got.Code != http.StatusNotFound {
			t.Fatalf("status %d after a stale lazy load; want 404 because no click was granted", got.Code)
		}
	})
}

func TestMediaStop(t *testing.T) {
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	server, _ := medTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	})
	medUseFake(t, server)
	client := newTestClient(t, false)
	app := client.app
	handler := client.Handler()
	origin := "https://example.com:" + medTestPort(t, server)
	p := medTestPostWith(medTestSlotAt(origin+"/a.png", medTestPNGSHA, medTestPNGSize))
	medUseRecord(t, p)
	medConsent(t, app, medTestProfile, origin)
	cookie := medTestCookie(medTestProfile)

	t.Run("stopping loading cancels a lazy load that is waiting on the origin", func(t *testing.T) {
		lazy := make(chan string, 1)
		go func() {
			lazy <- medPost(handler, url.Values{"post": {medTestPost}, "slot": {"a1"}, "auto": {"1"}}, true, cookie).Body.String()
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the lazy load never reached the server")
		}
		stop := medPostTo(handler, "/media/stop", url.Values{"post": {medTestPost}, "slot": {"a1"}, "origin": {origin}}, true, cookie)
		if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), "Not loaded") || strings.Contains(stop.Body.String(), `auto="lazy"`) {
			t.Fatalf("stop status %d with body %q; want the figure not loaded", stop.Code, stop.Body.String())
		}
		select {
		case <-cancelled:
		case <-time.After(10 * time.Second):
			t.Fatal("the lazy load kept running after its origin was stopped")
		}
		if body := <-lazy; strings.Contains(body, "<img") || strings.Contains(body, "could not be reached") {
			t.Fatalf("cancelled lazy load answered %q; want the figure not loaded", body)
		}
	})

	t.Run("after stopping, the post renders as not loaded with no lazy load", func(t *testing.T) {
		settings, err := app.settings(medTestProfile)
		if err != nil || len(settings.MediaOrigins) != 0 {
			t.Fatalf("stored origins = %v, %v; want none", settings.MediaOrigins, err)
		}
		html := string(renderPostMedia(app, medTestProfile, settings, p, time.Unix(0, 0)))
		if !strings.Contains(html, "Not loaded") || strings.Contains(html, `auto="lazy"`) {
			t.Fatalf("rendered post after stop = %s; want not loaded with no lazy load", html)
		}
	})
}

func TestMediaViewer(t *testing.T) {
	client := newTestClient(t, false)
	app := client.app
	handler := client.Handler()
	first := medTestSlotAt("https://example.com/a.png", medTestPNGSHA, medTestPNGSize)
	second := medTestSlotAt("https://example.com/b.png", medTestPNGSHA, medTestPNGSize)
	second.ID = "a2"
	medUseRecord(t, medTestPostWith(first, second))
	medStateFor(app).cache.put(medKey{sha: medTestPNGSHA, size: medTestPNGSize}, content.VerifiedMedia{Bytes: medTestPNGBytes(t), Type: content.MediaPNG, Width: 1, Height: 1})
	medConsent(t, app, medTestProfile, "https://example.com:443")

	t.Run("the viewer opens the slot asked for with previous navigation and thumbnails", func(t *testing.T) {
		w := medGet(handler, "/viewer/"+medTestPost+"?slot=a2", medTestCookie(medTestProfile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "2 / 2") || !strings.Contains(body, `name="slot" value="a1"`) || !strings.Contains(body, `aria-current="true"`) || !strings.Contains(body, `href="/posts/`+medTestPost+`"`) {
			t.Fatalf("status %d; viewer page lacks the second slot, previous control, current thumbnail or close link", w.Code)
		}
	})

	t.Run("the viewer's fragment answers an HTMLeX swap with the stage only", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/viewer/"+medTestPost+"?slot=a1", nil)
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.AddCookie(medTestCookie(medTestProfile))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="media-viewer"`) || strings.Contains(body, "<html") {
			t.Fatalf("status %d; fragment %q", w.Code, body)
		}
	})
}
