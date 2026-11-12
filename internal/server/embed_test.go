package server

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestPostEmbeds(t *testing.T) {
	check := func(t *testing.T, path, want string, status int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "untrusted.invalid"
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "untrusted.invalid") {
			t.Errorf("%s: status %d; wanted %d and %q without request host", path, w.Code, status, want)
		}
	}
	t.Run("post link", func(t *testing.T) { check(t, "/posts/maya", "community garden", http.StatusOK) })
	t.Run("embed content", func(t *testing.T) { check(t, "/embed/maya", "community garden", http.StatusOK) })
	t.Run("share code", func(t *testing.T) {
		check(t, "/share/fragment?id=maya", "https://example.test/embed/maya", http.StatusOK)
	})
	t.Run("second post", func(t *testing.T) { check(t, "/embed/elliot", "small things growing", http.StatusOK) })
	t.Run("technology video on post page", func(t *testing.T) { check(t, "/posts/tech-video", "demo-tech.mp4", http.StatusOK) })
	t.Run("unknown post", func(t *testing.T) { check(t, "/embed/unknown", "Post not found", http.StatusNotFound) })
}

func TestSharingNeedsPublicAddress(t *testing.T) {
	const notice = "Sharing needs a public address for this client"
	check := func(t *testing.T, cfg Config) {
		t.Helper()
		cfg.DataDir = t.TempDir()
		client, err := NewClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, notice) || strings.Contains(body, "<textarea") || strings.Contains(body, "/embed/maya") {
			t.Errorf("origin %q dev=%t: status %d; want the notice and no code", cfg.PublicOrigin, cfg.Dev, w.Code)
		}
	}
	t.Run("missing origin", func(t *testing.T) { check(t, Config{}) })
	t.Run("plain http on a public host", func(t *testing.T) { check(t, Config{PublicOrigin: "http://example.test"}) })
	t.Run("plain http loopback outside dev mode", func(t *testing.T) { check(t, Config{PublicOrigin: "http://127.0.0.1:8443"}) })
}

func TestDevLoopbackOriginShareLinks(t *testing.T) {
	client, err := NewClient(Config{Dev: true, DataDir: t.TempDir(), PublicOrigin: "http://127.0.0.1:8443"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `value="http://127.0.0.1:8443/posts/maya"`) || !strings.Contains(html.UnescapeString(body), `src="http://127.0.0.1:8443/embed/maya"`) {
		t.Errorf("dev loopback share panel: status %d; want links on the configured origin", w.Code)
	}
}

func TestVideoComponent(t *testing.T) {
	check := func(t *testing.T, path, asset string) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, asset) || !strings.Contains(body, `controls preload="none"`) {
			t.Errorf("%s: video asset or deferred loading missing (status %d)", path, w.Code)
		}
	}
	t.Run("feed video", func(t *testing.T) { check(t, "/?filter=video", "demo-tech.mp4") })
	t.Run("standalone video", func(t *testing.T) { check(t, "/posts/tech-video", "demo-tech.mp4") })
}

func TestEmbedFetchesNoMedia(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/tech-video", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, "demo-tech.mp4") || strings.Contains(body, "<video") || strings.Contains(body, "<img") || !strings.Contains(body, "Open on ForYou to load media.") {
		t.Errorf("embed of a video post: status %d; want an inert media note and no media element", w.Code)
	}
}

func TestEmbedHeadersAndDocument(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/maya", nil))
	body := w.Body.String()
	t.Run("content security policy", func(t *testing.T) {
		if got := w.Header().Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors *" {
			t.Errorf("Content-Security-Policy = %q", got)
		}
	})
	t.Run("referrer and caching", func(t *testing.T) {
		if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("Referrer-Policy %q, Cache-Control %q", w.Header().Get("Referrer-Policy"), w.Header().Get("Cache-Control"))
		}
	})
	t.Run("no cookie and no script", func(t *testing.T) {
		if w.Header().Get("Set-Cookie") != "" || strings.Contains(body, "app.js") || strings.Contains(body, "<script") {
			t.Errorf("embed sets a cookie or includes a script")
		}
	})
	t.Run("settlement badge, author and link to the post", func(t *testing.T) {
		if !strings.Contains(body, `class="detail-embed-link" href="/posts/maya" target="_blank" rel="noopener noreferrer"`) || !strings.Contains(body, "Open on ForYou") {
			t.Errorf("embed footer link missing")
		}
	})
}

func TestEmbedThemeChoice(t *testing.T) {
	h := testClientHandler(t)
	t.Run("explicit dark", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/maya?theme=dark", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-theme="dark"`) {
			t.Errorf("embed with theme=dark: status %d", w.Code)
		}
	})
	t.Run("unknown theme follows the reader", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/maya?theme=blue", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `data-theme=""`) {
			t.Errorf("embed with theme=blue: status %d", w.Code)
		}
	})
	t.Run("share code carries the chosen theme", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya&theme=dark", nil))
		if w.Code != http.StatusOK || !strings.Contains(html.UnescapeString(w.Body.String()), "https://example.test/embed/maya?theme=dark") {
			t.Errorf("share code for dark theme: status %d", w.Code)
		}
	})
}

func TestEmbedNotFoundCard(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/unknown", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "This post is not available from this client's sources.") {
		t.Errorf("unknown embed: status %d; want the not-available card", w.Code)
	}
}

func TestShareFieldsUseConfiguredOrigin(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `value="https://example.test/posts/maya"`) || !strings.Contains(body, "Copy the link or the embed code. They open only for people who can reach this ForYou server.") || strings.Contains(body, "data-copy-target") {
		t.Errorf("share panel fields or configured origin missing (status %d)", w.Code)
	}
}

func TestEmbedSnippetFitsEmbedCard(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
	body := w.Body.String()
	t.Run("iframe matches the 550px embed card", func(t *testing.T) {
		if w.Code != http.StatusOK || !strings.Contains(html.UnescapeString(body), `width="550" height="720" style="width:100%;max-width:550px;border:0"`) || strings.Contains(html.UnescapeString(body), `width="480"`) {
			t.Errorf("embed snippet dimensions: status %d; want 550 by 720 without the old 480 square", w.Code)
		}
	})
	t.Run("code box shows six lines", func(t *testing.T) {
		if !strings.Contains(body, `class="share-field share-code" id="embed-code-maya" readonly rows="6"`) {
			t.Errorf("embed code textarea does not have six rows")
		}
	})
}

func TestShareFallbackReturnsToPost(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share?id=maya", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/posts/maya"`) || strings.Contains(w.Body.String(), `get="/share/fragment"`) {
		t.Errorf("share page close does not return to its post as a plain form, status %d", w.Code)
	}
}

func TestStandalonePostWithoutTopic(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/journal", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `class="topic"`) || !strings.Contains(w.Body.String(), "rain.\n\nAfter a few weeks") {
		t.Errorf("standalone post lost paragraph text or showed an empty topic: status %d", w.Code)
	}
}

func TestDetailPagesUseTheAppShell(t *testing.T) {
	h := testClientHandler(t)
	checkShell := func(t *testing.T, path string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<nav`) || !strings.Contains(w.Body.String(), `aria-label="Primary"`) {
			t.Errorf("%s: status %d; app navigation landmark missing", path, w.Code)
		}
	}
	t.Run("post page", func(t *testing.T) { checkShell(t, "/posts/maya") })
	t.Run("thread page", func(t *testing.T) { checkShell(t, "/thread?id=maya") })
	t.Run("share page", func(t *testing.T) { checkShell(t, "/share?id=maya") })
	t.Run("gallery page", func(t *testing.T) { checkShell(t, "/gallery?id=sofia&slide=1") })
	t.Run("embed omits the shell", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/maya", nil))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `aria-label="Primary"`) {
			t.Errorf("embed page unexpectedly includes navigation (status %d)", w.Code)
		}
	})
}

func TestDetailNotFoundPages(t *testing.T) {
	h := testClientHandler(t)
	check := func(t *testing.T, path, want string, hasShell bool) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusNotFound || !strings.Contains(body, want) || strings.Contains(body, `aria-label="Primary"`) != hasShell {
			t.Errorf("%s: status %d; want 404 with %q and shell=%t", path, w.Code, want, hasShell)
		}
	}
	t.Run("post", func(t *testing.T) { check(t, "/posts/unknown", "Page not found", true) })
	t.Run("thread", func(t *testing.T) { check(t, "/thread?id=unknown", "Page not found", true) })
	t.Run("share", func(t *testing.T) { check(t, "/share?id=unknown", "Page not found", true) })
	t.Run("embed", func(t *testing.T) { check(t, "/embed/unknown", "Post not found", false) })
	t.Run("unknown gallery post", func(t *testing.T) { check(t, "/gallery?id=unknown&slide=1", "Page not found", true) })
	t.Run("post without gallery", func(t *testing.T) { check(t, "/gallery?id=journal&slide=1", "Page not found", true) })
}

func TestPostPageActionsStayInsideCard(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/maya", nil))
	body := w.Body.String()
	card := strings.Index(body, `<article class="post detail-post-card">`)
	actions := strings.Index(body, `detail-post-actions"`)
	thread := strings.Index(body, `id="thread-maya"`)
	end := -1
	if card >= 0 {
		end = strings.Index(body[card:], `</article>`)
	}
	if w.Code != http.StatusOK || card < 0 || actions < card || end < 0 || actions > card+end || thread < card+end || !strings.Contains(body, `aria-label="Like"`) || !strings.Contains(body, `>Embed</span>`) || !strings.Contains(body, `>Replies</span>`) {
		t.Errorf("post actions are not inside the post card, or the thread is not below it (status %d)", w.Code)
	}
}

func TestGalleryViewerControlsAndCounter(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gallery?id=sofia&slide=2", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "2 / 3") || !strings.Contains(body, "Image 2 of 3") || !strings.Contains(body, `alt="Illustrated mountain landscape, image 2 of 3"`) || !strings.Contains(body, `aria-label="Previous image"`) || !strings.Contains(body, `aria-label="Next image"`) {
		t.Errorf("gallery viewer counter or image text missing (status %d)", w.Code)
	}
}

func TestPostPageIgnoresInvalidThreadPage(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/maya?page=3", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="thread-maya"`) || strings.Contains(w.Body.String(), `id="thread-"`) {
		t.Errorf("invalid embedded thread page was not reset (status %d)", w.Code)
	}
}

// embSeededClient serves one signed network post from a fixture provider whose trust root is ready.
func embSeededClient(t *testing.T) (*Client, string) {
	t.Helper()
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	recordRaw, providerID := signedNetworkRecord(t, now.Add(-24*time.Hour))
	var message wire.TransportMessage
	if err := wire.Decode(recordRaw, "TransportMessage", &message); err != nil {
		t.Fatal(err)
	}
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: now, Endpoint: "https://foundation.example"})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/messages/"):
			_, _ = w.Write(recordRaw)
		case strings.HasPrefix(r.URL.Path, "/v1/replies/"):
			_ = json.NewEncoder(w).Encode(wire.DiscoveryPage{GeneratedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Messages: []wire.PublicTransportMessage{}, NextCursor: nil})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test", Now: func() time.Time { return now }, HTTPClient: provider.Client(), Providers: []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Fixture provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	client.app.root = root
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: now, Endpoint: "https://foundation.example"}
	client.app.trustMu.Unlock()
	return client, message.MessageID
}

func TestSeededRecordShareAndEmbed(t *testing.T) {
	client, id := embSeededClient(t)
	h := client.Handler()
	t.Run("share panel has the post link and iframe code with the full message ID", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id="+id, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `value="https://example.test/posts/`+id+`"`) || !strings.Contains(html.UnescapeString(body), `<iframe src="https://example.test/embed/`+id+`" title="ForYou post by `) {
			t.Errorf("share panel for a seeded record: status %d", w.Code)
		}
	})
	t.Run("share page shows the same code", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share?id="+id, nil))
		if w.Code != http.StatusOK || !strings.Contains(html.UnescapeString(w.Body.String()), "https://example.test/embed/"+id) {
			t.Errorf("share page for a seeded record: status %d", w.Code)
		}
	})
	t.Run("embed shows the verified card with its settlement badge", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/"+id, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "A verified network post.") || !strings.Contains(w.Body.String(), "Pending") || !strings.Contains(w.Body.String(), "Not proof of payment.") {
			t.Errorf("embed of a seeded record: status %d", w.Code)
		}
	})
	t.Run("non-canonical message ID is not found", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id="+strings.ToUpper(id), nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("uppercase message ID: status %d; want 404", w.Code)
		}
	})
	t.Run("share controls address the panel by a colon-free key", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id="+id, nil))
		body := w.Body.String()
		key := strings.ReplaceAll(id, ":", "-")
		if w.Code != http.StatusOK || !strings.Contains(body, `target="#share-`+key+`(innerHTML)"`) || strings.Contains(body, `share-`+id) {
			t.Errorf("share controls for a message ID with a colon: status %d; want the key %s in every target and no raw ID in an element ID", w.Code, key)
		}
	})
}

func TestEmbedOfSpecificationMediaSlotsLoadsNothing(t *testing.T) {
	// Literal slot values copied from ../ForYou-Social-Ecosystem-Specification/examples/post-with-media-slots.json.
	photoAlt, clipAlt := "Sunset", "Short clip"
	slots := []wire.MediaReference{
		{ID: "photo", URL: "https://media.example/sunset.jpg", Kind: "image", SHA256: "03c40629f930724f4724705524fc8c3d878697a8c4a6210ac4df06fcebfd9c44", AltText: &photoAlt},
		{ID: "clip", URL: "https://media.example/clip.mp4", Kind: "video", SHA256: "4280f6bae17c31d302094e79f4c75a55c2c5bccf1067241a9454efed447f3a88", AltText: &clipAlt},
	}
	message := wire.TransportMessage{
		MessageID:   "fy1:8ce9fcf3b498398bcd9a5c62345257c421037e8cfbb58418c32dc56b496dc15f",
		AuthorID:    "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd",
		Timestamp:   "2026-11-11T00:00:04Z",
		MessageType: "post",
		Payload:     wire.PostPayload{ContentFormat: "text/markdown", Text: "An image: ![Sunset](fy-media:photo)", MediaSlots: &slots},
	}
	p := postFromRecord(sdk.Record{Message: message}, time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC))
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := client.app.templates.ExecuteTemplate(&out, "embed-page", shareDetails{Post: p}); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	t.Run("no slot URL is fetched", func(t *testing.T) {
		if strings.Contains(body, `src="https://media.example/sunset.jpg"`) || strings.Contains(body, `src="https://media.example/clip.mp4"`) {
			t.Errorf("embed of the specification media slots references a slot URL in src")
		}
	})
	t.Run("no media element is rendered", func(t *testing.T) {
		if strings.Contains(body, "<img") || strings.Contains(body, "<video") {
			t.Errorf("embed of the specification media slots renders an image or video element")
		}
	})
	t.Run("the reader is told media is not loaded", func(t *testing.T) {
		if !strings.Contains(body, "Open on ForYou to load media.") {
			t.Errorf("embed of the specification media slots has no media note")
		}
	})
}

func TestEmbedNamesTheAuthor(t *testing.T) {
	t.Run("demo post uses the author's name", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
		if w.Code != http.StatusOK || !strings.Contains(html.UnescapeString(w.Body.String()), `title="ForYou post by Maya Chen"`) {
			t.Errorf("share code for maya: status %d; want the author's name in the iframe title", w.Code)
		}
	})
	t.Run("unnamed network post uses the author key fingerprint", func(t *testing.T) {
		client, id := embSeededClient(t)
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id="+id, nil))
		if w.Code != http.StatusOK || !regexp.MustCompile(`title="ForYou post by [0-9a-f]{4} [0-9a-f]{4} [0-9a-f]{4} [0-9a-f]{4}"`).MatchString(html.UnescapeString(w.Body.String())) {
			t.Errorf("share code for a network post: status %d; want the author fingerprint in the iframe title", w.Code)
		}
	})
}
