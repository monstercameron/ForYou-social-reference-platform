package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/rank"
)

func fyoVote(id, vote string, at int) *http.Cookie {
	return &http.Cookie{Name: feedbackCookieName(id), Value: vote + "." + strconv.Itoa(at)}
}

func fyoForYouCookie() *http.Cookie {
	return &http.Cookie{Name: fyoOrderCookie, Value: fyoForYou}
}

// fyoActiveCookies is the For you order with enough votes for the model to be active.
func fyoActiveCookies() []*http.Cookie {
	return []*http.Cookie{
		fyoForYouCookie(),
		fyoVote("garden", "up", 1), fyoVote("science", "up", 2), fyoVote("music", "up", 3), fyoVote("pets", "up", 4), fyoVote("art-studio", "up", 5),
		fyoVote("scam", "down", 6), fyoVote("harassment", "down", 7), fyoVote("bullying", "down", 8), fyoVote("adult-1", "down", 9), fyoVote("adult-2", "down", 10),
	}
}

// fyoPressed reports whether the button with this label is marked pressed.
func fyoPressed(body, label string) bool {
	end := strings.Index(body, ">"+label+"</button>")
	if end < 0 {
		return false
	}
	start := strings.LastIndex(body[:end], "<button")
	return start >= 0 && strings.Contains(body[start:end], `aria-pressed="true"`)
}

func fyoCookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var fyoSampleNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func fyoSampleModel() rank.Model {
	return rank.Train([]rank.Vote{
		{PostID: "g1", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "g2", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "g3", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "g4", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "g5", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "g6", Like: true, At: fyoSampleNow, Indicators: []string{"topic:garden"}},
		{PostID: "s1", Like: false, At: fyoSampleNow, Indicators: []string{"topic:sports"}},
		{PostID: "s2", Like: false, At: fyoSampleNow, Indicators: []string{"topic:sports"}},
		{PostID: "s3", Like: false, At: fyoSampleNow, Indicators: []string{"topic:sports"}},
		{PostID: "s4", Like: false, At: fyoSampleNow, Indicators: []string{"topic:sports"}},
	}, fyoSampleNow, nil)
}

var fyoSampleSets = [][]string{{"topic:sports"}, {}, {}, {"topic:garden"}}

func fyoSamplePosts() []post {
	return []post{{ID: "A"}, {ID: "B"}, {ID: "D"}, {ID: "C"}}
}

func TestFyoArrange(t *testing.T) {
	t.Run("orders by score and keeps newest first on ties", func(t *testing.T) {
		posts := fyoSamplePosts()
		fyoArrange(posts, fyoSampleModel(), fyoSampleSets)
		if posts[0].ID != "C" || posts[1].ID != "B" || posts[2].ID != "D" || posts[3].ID != "A" {
			t.Errorf("order = %s, %s, %s, %s; want C, B, D, A", posts[0].ID, posts[1].ID, posts[2].ID, posts[3].ID)
		}
	})
	t.Run("explains in plain words", func(t *testing.T) {
		posts := fyoSamplePosts()
		fyoArrange(posts, fyoSampleModel(), fyoSampleSets)
		if posts[0].Why != "Why this post: you tend to like posts about garden." {
			t.Errorf("liked why = %q", posts[0].Why)
		}
		if posts[3].Why != "Why this post: you tend to dislike posts about sports." {
			t.Errorf("disliked why = %q", posts[3].Why)
		}
		if posts[1].Why != "Why this post: nothing you liked or disliked matches it yet." {
			t.Errorf("unmatched why = %q", posts[1].Why)
		}
	})
	t.Run("keeps an existing why", func(t *testing.T) {
		posts := fyoSamplePosts()
		posts[3].Why = "Because of a post you liked"
		fyoArrange(posts, fyoSampleModel(), fyoSampleSets)
		if posts[0].ID != "C" || posts[0].Why != "Because of a post you liked" {
			t.Errorf("why = %q; want the existing why kept", posts[0].Why)
		}
	})
}

func TestFyoPhrase(t *testing.T) {
	t.Run("topic is about", func(t *testing.T) {
		if got := fyoPhrase("topic:garden"); got != "posts about garden" {
			t.Errorf("phrase = %q", got)
		}
	})
	t.Run("author key", func(t *testing.T) {
		if got := fyoPhrase("key:did:example"); got != "posts by this author" {
			t.Errorf("phrase = %q", got)
		}
	})
	t.Run("unknown indicator", func(t *testing.T) {
		if got := fyoPhrase("odd:thing"); got != "posts tagged odd:thing" {
			t.Errorf("phrase = %q", got)
		}
	})
}

func TestFyoOrderRoute(t *testing.T) {
	h := testClientHandler(t)
	t.Run("saves For you and redirects home", func(t *testing.T) {
		w := postForm(h, "/feed/order", url.Values{"order": {"foryou"}, "filter": {"all"}})
		cookie := fyoCookieNamed(w, fyoOrderCookie)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
			t.Fatalf("response = %d to %q; want 303 to /", w.Code, w.Header().Get("Location"))
		}
		if cookie == nil || cookie.Value != fyoForYou || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie = %+v; want foryou_order=foryou, HttpOnly, SameSite=Strict", cookie)
		}
	})
	t.Run("keeps filter and search", func(t *testing.T) {
		w := postForm(h, "/feed/order", url.Values{"order": {"foryou"}, "filter": {"text"}, "q": {"jupiter"}})
		if w.Header().Get("Location") != "/?filter=text&q=jupiter" {
			t.Errorf("location = %q; want /?filter=text&q=jupiter", w.Header().Get("Location"))
		}
	})
	t.Run("Newest removes the cookie", func(t *testing.T) {
		w := postForm(h, "/feed/order", url.Values{"order": {"newest"}})
		if cookie := fyoCookieNamed(w, fyoOrderCookie); cookie == nil || cookie.MaxAge >= 0 {
			t.Errorf("cookie = %+v; want it expired", cookie)
		}
	})
	t.Run("rejects an unknown order", func(t *testing.T) {
		w := postForm(h, "/feed/order", url.Values{"order": {"best"}})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Choose Newest or For you.") {
			t.Errorf("response = %d %q; want 400 with the choice message", w.Code, w.Body.String())
		}
	})
	t.Run("rejects a cross-site post", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/feed/order", strings.NewReader(url.Values{"order": {"foryou"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || fyoCookieNamed(w, fyoOrderCookie) != nil {
			t.Errorf("response = %d with cookie; want 403 and no cookie", w.Code)
		}
	})
	t.Run("fragment returns the feed section", func(t *testing.T) {
		w := postForm(h, "/feed/order/fragment", url.Values{"order": {"foryou"}, "filter": {"all"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="feed-section"`) || !fyoPressed(body, "For you") {
			t.Errorf("fragment = %d; want the feed section with For you pressed", w.Code)
		}
	})
}

func TestFyoHome(t *testing.T) {
	h := testClientHandler(t)
	t.Run("Newest by default", func(t *testing.T) {
		body := getWith(h, "/")
		if !strings.Contains(body, `id="fyo-bar"`) || !fyoPressed(body, "Newest") || strings.Contains(body, "Why this post:") {
			t.Error("home should show the sort bar with Newest pressed and no Why lines")
		}
	})
	t.Run("damaged cookie falls back to Newest", func(t *testing.T) {
		body := getWith(h, "/", &http.Cookie{Name: fyoOrderCookie, Value: "%%%"})
		if !strings.Contains(body, `id="fyo-bar"`) || !fyoPressed(body, "Newest") {
			t.Error("a damaged order cookie should show Newest")
		}
	})
	t.Run("too few votes stays newest", func(t *testing.T) {
		body := getWith(h, "/", fyoForYouCookie(), fyoVote("garden", "up", 1), fyoVote("science", "up", 2), fyoVote("music", "up", 3))
		_, after, _ := strings.Cut(body, `href="/posts/`)
		first, _, _ := strings.Cut(after, `"`)
		if !strings.Contains(body, "You have 3 likes and 0 dislikes.") || strings.Contains(body, "Why this post:") || first != "maya" {
			t.Errorf("first post = %q; want maya, the newest order, with the too-few-votes note", first)
		}
	})
	t.Run("enough votes explains each card", func(t *testing.T) {
		body := getWith(h, "/", fyoActiveCookies()...)
		if !strings.Contains(body, "from your 5 likes and 5 dislikes") || !strings.Contains(body, "Why this post:") {
			t.Error("For you should explain its cards once the model is active")
		}
	})
	t.Run("explore is not reordered", func(t *testing.T) {
		body := getWith(h, "/explore", fyoActiveCookies()...)
		if strings.Contains(body, "Why this post:") || strings.Contains(body, `id="fyo-bar"`) {
			t.Error("explore should keep its own order and show no sort bar")
		}
	})
	t.Run("ads keep their own order", func(t *testing.T) {
		body := getWith(h, "/?filter=ads", fyoActiveCookies()...)
		if strings.Contains(body, "Why this post:") || strings.Contains(body, `id="fyo-bar"`) {
			t.Error("the ads section should keep its own order and show no sort bar")
		}
	})
}
