package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestTopicsLinks(t *testing.T) {
	t.Run("splits the rubric topics into links", func(t *testing.T) {
		got := rating{Topic: "garden, art"}.TopicLinks()
		want := []topicsLink{{Slug: "garden", Label: "Garden", Href: "/topics/garden"}, {Slug: "art", Label: "Art", Href: "/topics/art"}}
		if !slices.Equal(got, want) {
			t.Errorf("TopicLinks() = %+v; want %+v", got, want)
		}
	})
	t.Run("a post without topics has no links", func(t *testing.T) {
		if got := (rating{}).TopicLinks(); got != nil {
			t.Errorf("TopicLinks() = %+v; want nil", got)
		}
	})
	t.Run("a malformed topic is skipped", func(t *testing.T) {
		got := rating{Topic: "garden, Bad1"}.TopicLinks()
		want := []topicsLink{{Slug: "garden", Label: "Garden", Href: "/topics/garden"}}
		if !slices.Equal(got, want) {
			t.Errorf("TopicLinks() = %+v; want %+v", got, want)
		}
	})
}

func TestTopicsCounts(t *testing.T) {
	posts := []post{
		{Rating: rating{Topic: "garden, art"}},
		{Rating: rating{Topic: "art"}},
		{},
	}
	got := topicsCounts(posts)
	want := []topicsCount{
		{Slug: "art", Label: "Art", Href: "/topics/art", Posts: 2},
		{Slug: "garden", Label: "Garden", Href: "/topics/garden", Posts: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("topicsCounts() = %+v; want %+v", got, want)
	}
}

func topicsGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestTopicsPage(t *testing.T) {
	h := testClientHandler(t)
	t.Run("lists the fetched posts with the topic", func(t *testing.T) {
		w := topicsGet(h, "/topics/garden")
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<h1 id="topics-title">Garden</h1>`) || !strings.Contains(body, "2 of the 44 posts this client fetched have this topic.") || !strings.Contains(body, `href="/posts/garden"`) || !strings.Contains(body, `href="/posts/art-studio"`) || strings.Contains(body, `href="/posts/pets"`) {
			t.Errorf("GET /topics/garden: status %d; the page should list the two garden posts and no others", w.Code)
		}
	})
	t.Run("reader filters still fold posts", func(t *testing.T) {
		if body := topicsGet(h, "/topics/art").Body.String(); !strings.Contains(body, "Folded by your filters") {
			t.Error("the adult-labelled post with the art topic should be folded by the default filters")
		}
	})
	t.Run("valid topic with no posts", func(t *testing.T) {
		w := topicsGet(h, "/topics/knitting")
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "No fetched posts with this topic") || !strings.Contains(body, "None of the 44 posts this client fetched has the topic “knitting”.") {
			t.Errorf("GET /topics/knitting: status %d; want the empty state naming the fetched posts", w.Code)
		}
		if !strings.Contains(body, "<p>Posts this client fetched with this topic.") || strings.Contains(body, "0 of the 44") {
			t.Error("GET /topics/knitting: the subtitle must not count matches when there are none")
		}
	})
	t.Run("malformed topic is not found", func(t *testing.T) {
		if code := topicsGet(h, "/topics/Garden").Code; code != http.StatusNotFound {
			t.Errorf("GET /topics/Garden: status %d; want 404", code)
		}
		if code := topicsGet(h, "/topics/a1").Code; code != http.StatusNotFound {
			t.Errorf("GET /topics/a1: status %d; want 404", code)
		}
	})
	t.Run("never says trending", func(t *testing.T) {
		body := topicsGet(h, "/topics/garden").Body.String()
		if strings.Contains(body, "rending") || strings.Contains(body, "network total") {
			t.Error("a topic page must count fetched posts only and never describe a trend or a network total")
		}
	})
}

func TestTopicsIndexAndRail(t *testing.T) {
	h := testClientHandler(t)
	t.Run("index counts fetched posts", func(t *testing.T) {
		body := topicsGet(h, "/topics").Body.String()
		if !strings.Contains(body, "Topics in your feed") || !strings.Contains(body, "Topics the rubric found in the 44 posts this client fetched.") || !strings.Contains(body, `href="/topics/art"><span class="topics-row-name">Art</span><span class="topics-row-count">3 posts</span>`) {
			t.Error("the index should list the topics with their counts over the fetched posts")
		}
	})
	t.Run("rail fragment lists eight", func(t *testing.T) {
		body := topicsGet(h, "/topics/rail").Body.String()
		if !strings.Contains(body, `href="/topics/crypto"`) || strings.Contains(body, `href="/topics/games"`) || !strings.Contains(body, "Counted over the 44 posts this client fetched.") || strings.Contains(body, "<html") {
			t.Error("GET /topics/rail should return the fragment with the first eight topics, not a page")
		}
	})
}

func TestTopicsHooks(t *testing.T) {
	h := testClientHandler(t)
	t.Run("home cards carry chips", func(t *testing.T) {
		if body := topicsGet(h, "/").Body.String(); !strings.Contains(body, `<ul class="topics-chips" aria-label="Topics">`) {
			t.Error("a feed card with a rubric topic should show its topic chips")
		}
	})
	t.Run("home rail has the panel with a fallback", func(t *testing.T) {
		body := topicsGet(h, "/").Body.String()
		if !strings.Contains(body, `<h2 id="topics-rail-title">Topics in your feed</h2>`) || !strings.Contains(body, `get="/topics/rail"`) || !strings.Contains(body, `action="/topics"`) {
			t.Error("the home right rail should hold the topics panel with a plain link fallback")
		}
	})
	t.Run("topic pages hide the rail panel", func(t *testing.T) {
		if body := topicsGet(h, "/topics/garden").Body.String(); strings.Contains(body, "topics-rail-title") {
			t.Error("a topic page should not repeat the rail panel")
		}
	})
}
