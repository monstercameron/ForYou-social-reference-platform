package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReplyMarkdown(t *testing.T) {
	render := func(text string) string {
		markup, _ := replyMarkup(text)
		return string(markup)
	}
	contains := func(t *testing.T, text, want string) {
		t.Helper()
		if got := render(text); !strings.Contains(got, want) {
			t.Errorf("markup of %q = %q; want it to contain %q", text, got, want)
		}
	}
	t.Run("emphasis", func(t *testing.T) { contains(t, "**bold** and *soft*", "<strong>bold</strong> and <em>soft</em>") })
	t.Run("code span", func(t *testing.T) { contains(t, "run `go test`", "<code>go test</code>") })
	t.Run("code block keeps its lines and escapes markup", func(t *testing.T) {
		contains(t, "```go\nif a < b {\n\treturn\n}\n```", "<pre><code>if a &lt; b {\n\treturn\n}\n</code></pre>")
	})
	t.Run("list", func(t *testing.T) { contains(t, "- one\n- two", "<ul>\n<li>one</li>\n<li>two</li>\n</ul>") })
	t.Run("quote", func(t *testing.T) { contains(t, "> said before", "<blockquote>\n<p>said before</p>\n</blockquote>") })
	t.Run("raw HTML is shown as text", func(t *testing.T) {
		got := render("<script>alert(1)</script> <b>x</b>")
		if strings.Contains(got, "<script") || strings.Contains(got, "<b>") || !strings.Contains(got, "&lt;script&gt;") {
			t.Errorf("raw HTML reached the page: %q", got)
		}
	})
	t.Run("links stay inert", func(t *testing.T) {
		got := render("[click](https://example.com/a) and [bad](javascript:alert(1))")
		if strings.Contains(got, "<a ") || strings.Contains(got, "href") {
			t.Errorf("a reply link must not be clickable yet: %q", got)
		}
	})
	t.Run("external image is not loaded", func(t *testing.T) {
		got, pictures := replyMarkup("![x](https://tracker.example/pixel.png)")
		if strings.Contains(string(got), "<img") || len(pictures) != 0 {
			t.Errorf("an external image must stay text: %q, %v", got, pictures)
		}
	})
	t.Run("declared slot becomes a picture", func(t *testing.T) {
		_, pictures := replyMarkup("One ![a](fy-media:landscape-2) two ![b](fy-media:landscape-3) and ![c](fy-media:unknown)")
		if len(pictures) != 2 || pictures[0] != 2 || pictures[1] != 3 {
			t.Errorf("pictures = %v; want the two declared slots in text order", pictures)
		}
	})
}

func TestMarkdownInThreads(t *testing.T) {
	h := newHandler("client", nil)
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code, w.Body.String()
	}
	t.Run("fixture formatting", func(t *testing.T) {
		_, body := get("/thread/fragment?id=travel&page=2&open=0.0")
		if !strings.Contains(body, "The <strong>18:40</strong>.") || !strings.Contains(body, "<li>Buy the ticket on board</li>") || !strings.Contains(body, "<blockquote>") || !strings.Contains(body, "<code>Harbor path, east gate</code>") {
			t.Errorf("the travel thread should show bold text, a list, a quote and a code span")
		}
	})
	t.Run("fenced code no longer shows its fences", func(t *testing.T) {
		_, body := get("/thread/fragment?id=open-source&page=2&open=0.0.0")
		if strings.Contains(body, "```") || !strings.Contains(body, "<pre><code>func resolve(root, file, href string) string {") {
			t.Error("a fenced block should render as a code block")
		}
	})
	t.Run("reply picture opens the viewer", func(t *testing.T) {
		_, body := get("/thread/fragment?id=photography&page=2")
		want := `<input type="hidden" name="id" value="photography"><input type="hidden" name="reply" value="1"><input type="hidden" name="slide" value="2">`
		if !strings.Contains(body, want) || !strings.Contains(body, `get="/gallery/viewer" target="#viewer(innerHTML)"`) || strings.Count(body, `class="image-open"`) != 2 {
			t.Errorf("the reply with two pictures should offer both to the viewer")
		}
	})
	t.Run("written reply is previewed and posted as Markdown", func(t *testing.T) {
		text := "A **firm** yes\n\n- with a list"
		preview := postForm(h, "/reply/quote/fragment", url.Values{"id": {"noor"}, "parent": {""}, "text": {text}})
		if !strings.Contains(preview.Body.String(), "A <strong>firm</strong> yes") {
			t.Errorf("the confirmation should preview the Markdown: %s", preview.Body.String())
		}
		posted := postForm(h, "/reply/fragment", url.Values{"id": {"noor"}, "quote": {quoteReplyForTest(t, h, "noor", "", text)}})
		if !strings.Contains(posted.Body.String(), "<li>with a list</li>") {
			t.Error("the posted reply should be rendered as Markdown")
		}
	})
}

func TestImageViewer(t *testing.T) {
	h := newHandler("client", nil)
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code, w.Body.String()
	}
	hasSlot := func(t *testing.T, path string) {
		t.Helper()
		if _, body := get(path); strings.Count(body, `<div id="viewer" class="viewer-slot"></div>`) != 1 {
			t.Errorf("%s should hold exactly one empty viewer slot", path)
		}
	}
	t.Run("home has the viewer slot", func(t *testing.T) { hasSlot(t, "/") })
	t.Run("explore has the viewer slot", func(t *testing.T) { hasSlot(t, "/explore") })
	t.Run("post page has the viewer slot", func(t *testing.T) { hasSlot(t, "/posts/sofia") })
	t.Run("thread page has the viewer slot", func(t *testing.T) { hasSlot(t, "/thread?id=sofia") })
	t.Run("slideshow slide", func(t *testing.T) {
		code, body := get("/gallery/viewer?id=sofia&slide=2")
		if code != http.StatusOK || !strings.Contains(body, `role="dialog" aria-modal="true"`) || !strings.Contains(body, "demo-landscape-2.svg") || !strings.Contains(body, "2 / 3") || !strings.Contains(body, `name="slide" value="3"`) || !strings.Contains(body, `name="slide" value="1"`) || !strings.Contains(body, "data-viewer-close") || !strings.Contains(body, "A few quiet moments from the mountains.") {
			t.Errorf("status %d; the viewer should show slide 2 with its caption, both neighbours and a close control", code)
		}
	})
	t.Run("single image has no slide controls", func(t *testing.T) {
		_, body := get("/gallery/viewer?id=travel&slide=1")
		if strings.Contains(body, "data-gallery-direction") || !strings.Contains(body, "demo-landscape-3.svg") {
			t.Error("a single image needs no previous or next control")
		}
	})
	t.Run("reply picture", func(t *testing.T) {
		code, body := get("/gallery/viewer?id=photography&reply=1&slide=2")
		if code != http.StatusOK || !strings.Contains(body, "demo-landscape-3.svg") || !strings.Contains(body, `name="reply" value="1"`) || !strings.Contains(body, "2 / 2") {
			t.Errorf("status %d; the viewer should show the reply's second picture and keep the reply in its controls", code)
		}
	})
	t.Run("close empties the slot", func(t *testing.T) {
		code, body := get("/gallery/viewer?view=closed")
		if code != http.StatusOK || strings.Contains(body, "viewer") {
			t.Errorf("status %d, body %q; closing should return no viewer", code, body)
		}
	})
	missing := func(t *testing.T, path string) {
		t.Helper()
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("%s: status %d; want 404", path, code)
		}
	}
	t.Run("slide past the end", func(t *testing.T) { missing(t, "/gallery/viewer?id=sofia&slide=4") })
	t.Run("post without pictures", func(t *testing.T) { missing(t, "/gallery/viewer?id=journal&slide=1") })
	t.Run("reply without pictures", func(t *testing.T) { missing(t, "/gallery/viewer?id=photography&reply=0&slide=1") })
	t.Run("unknown reply", func(t *testing.T) { missing(t, "/gallery/viewer?id=photography&reply=9&slide=1") })
	t.Run("page without scripts is wide", func(t *testing.T) {
		_, body := get("/gallery?id=photography&reply=1&slide=1")
		if !strings.Contains(body, `class="detail-column viewer-column"`) || !strings.Contains(body, "demo-landscape-2.svg") || !strings.Contains(body, `name="reply" value="1"`) {
			t.Error("the plain gallery page should show the reply picture in the wide column")
		}
	})
}
