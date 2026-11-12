package server

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func expGet(t *testing.T, handler http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

// expNetworkClient is a client whose provider streamed two records by the
// fixture author, one signed on 2026-11-11 and one on 2026-11-10, and holds a
// key succession the fixture author signed.
func expNetworkClient(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	recentRaw, _ := nrTestRecord(t, "Garden beds dug on the eleventh.", nrTestNow.Add(-time.Hour), "")
	olderRaw, _ := nrTestRecord(t, "Tomatoes staked on the tenth.", nrTestNow.Add(-30*time.Hour), "")
	oldKey := ed25519.NewKeyFromSeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	newKey := ed25519.NewKeyFromSeed([]byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191})
	handover, err := succession.New(oldKey, newKey, nrTestNow.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	successions, err := canon.Encode(map[string][]wire.KeySuccession{"successions": {handover}})
	if err != nil {
		t.Fatal(err)
	}
	author, successor := keys.DIDOf(oldKey), keys.DIDOf(newKey)
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/changes":
			nrTestJSON(w, http.StatusOK, `{"items":[{"message":`+string(recentRaw)+`,"sequence":1},{"message":`+string(olderRaw)+`,"sequence":2}],"more":false,"next_after":2,"provider_id":"`+providerID+`"}`)
		case "/v1/successions/" + author:
			nrTestJSON(w, http.StatusOK, string(successions))
		default:
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	clock := nrTestNow
	client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
	if err := client.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client.Handler(), author, successor
}

func TestExplorePage(t *testing.T) {
	t.Run("initial page shows the heading, both forms and recent records", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore")
		if status != http.StatusOK || !strings.Contains(body, `<h1 id="explore-title">Explore</h1>`) || !strings.Contains(body, "Search the posts this client has fetched, or look up an author by their key. There is no global search.") || !strings.Contains(body, `role="search"`) || !strings.Contains(body, `<label for="explore-q">Search fetched posts</label>`) || !strings.Contains(body, "Look up an author") || !strings.Contains(body, "The tomatoes finally ripened") {
			t.Errorf("initial Explore page: status %d", status)
		}
	})
	t.Run("navigation marks Explore and keeps the theme switch", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q=server")
		if status != http.StatusOK || !strings.Contains(body, `href="/explore" aria-current="page"`) || !strings.Contains(body, `href="/theme?choice=light&amp;back=%2fexplore%3fq%3dserver"`) || !strings.Contains(body, `action="/explore"`) {
			t.Errorf("Explore navigation or theme context missing, status %d", status)
		}
	})
}

func TestExploreSearch(t *testing.T) {
	t.Run("garden returns only the posts that contain it", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q=garden")
		if status != http.StatusOK || !strings.Contains(body, "like a community garden?") || strings.Contains(body, "The tomatoes finally ripened") || !strings.Contains(body, `value="garden"`) {
			t.Errorf("search for garden: status %d", status)
		}
	})
	t.Run("kind video keeps only video posts", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?kind=video")
		if status != http.StatusOK || !strings.Contains(body, "A quick video test from my home lab") || strings.Contains(body, "The tomatoes finally ripened") || !strings.Contains(body, `value="video" checked`) {
			t.Errorf("kind=video: status %d", status)
		}
	})
	t.Run("an over-long query is 400 with the field error", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q="+strings.Repeat("a", 121))
		if status != http.StatusBadRequest || !strings.Contains(body, `<p id="explore-error" class="notice-bad explore-error" role="alert">Search text can be at most 120 characters.</p>`) || !strings.Contains(body, `aria-describedby="explore-q-help explore-error" aria-invalid="true"`) {
			t.Errorf("long query: status %d", status)
		}
	})
	t.Run("an unknown kind is 400", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?kind=bad")
		if status != http.StatusBadRequest || !strings.Contains(body, "Choose a kind from the list.") {
			t.Errorf("unknown kind: status %d", status)
		}
	})
	t.Run("an @name typed into search is plain text", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q=%40maya")
		if status != http.StatusOK || !strings.Contains(body, "No fetched posts match. Your sources may have others.") || strings.Contains(body, "Enter a full did:key") {
			t.Errorf("@maya search: status %d", status)
		}
	})
	t.Run("coverage names the held records and the source", func(t *testing.T) {
		handler, _, _ := expNetworkClient(t)
		status, body := expGet(t, handler, "/explore?q=garden")
		if status != http.StatusOK || !strings.Contains(body, "Searching 2 records this server has fetched from Provider.") || !strings.Contains(body, "Garden beds dug on the eleventh.") || strings.Contains(body, "Tomatoes staked on the tenth.") {
			t.Errorf("network search: status %d", status)
		}
	})
	t.Run("a post just published is found", func(t *testing.T) {
		client := newTestClient(t, false)
		store := &demoStore{}
		if _, err := store.add("newly created explore record", "explore-token"); err != nil {
			t.Fatal(err)
		}
		client.app.store = store
		status, body := expGet(t, client.Handler(), "/explore?q=newly+created")
		if status != http.StatusOK || !strings.Contains(body, "newly created explore record") {
			t.Errorf("new post missing from Explore: status %d", status)
		}
	})
}

func TestExploreDates(t *testing.T) {
	t.Run("one day becomes its UTC bounds and filters by signed time", func(t *testing.T) {
		handler, _, _ := expNetworkClient(t)
		status, body := expGet(t, handler, "/explore?from=2026-11-11&until=2026-11-11")
		if status != http.StatusOK || !strings.Contains(body, "UTC 2026-11-11 to 2026-11-11") || !strings.Contains(body, "Garden beds dug on the eleventh.") || strings.Contains(body, "Tomatoes staked on the tenth.") {
			t.Errorf("date range: status %d", status)
		}
	})
	t.Run("From after Until is 400", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?from=2026-11-12&until=2026-11-11")
		if status != http.StatusBadRequest || !strings.Contains(body, "From must be on or before Until.") {
			t.Errorf("reversed range: status %d", status)
		}
	})
}

func TestExploreAuthor(t *testing.T) {
	t.Run("the author's key shows the header and only that author's records", func(t *testing.T) {
		handler, author, _ := expNetworkClient(t)
		status, body := expGet(t, handler, "/explore?author="+author)
		if status != http.StatusOK || !strings.Contains(body, `readonly value="`+author+`"`) || !strings.Contains(body, "Posts by this key from your sources") || !strings.Contains(body, "Garden beds dug on the eleventh.") || !strings.Contains(body, "Tomatoes staked on the tenth.") || !strings.Contains(body, "Local copy: 2 more held records match") {
			t.Errorf("author lookup: status %d", status)
		}
	})
	t.Run("another key shows none of them", func(t *testing.T) {
		handler, _, successor := expNetworkClient(t)
		status, body := expGet(t, handler, "/explore?author="+successor)
		if status != http.StatusOK || !strings.Contains(body, "Your sources returned no posts by this key.") || strings.Contains(body, "Garden beds dug on the eleventh.") || strings.Contains(body, "This key names a successor") {
			t.Errorf("other author: status %d", status)
		}
	})
	t.Run("a succession the old key signed is shown", func(t *testing.T) {
		handler, author, successor := expNetworkClient(t)
		status, body := expGet(t, handler, "/explore?author="+author)
		if status != http.StatusOK || !strings.Contains(body, "This key names a successor: "+successor+" (asserted by the old key)") {
			t.Errorf("successor line: status %d", status)
		}
	})
	t.Run("an @handle is rejected with the identity sentence", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?author=%40maya")
		if status != http.StatusBadRequest || !strings.Contains(body, "Enter a full did:key; names and @handles are not identities.") || !strings.Contains(body, `aria-describedby="explore-author-help explore-error" aria-invalid="true"`) {
			t.Errorf("handle: status %d", status)
		}
	})
}

func TestExploreLoadMore(t *testing.T) {
	t.Run("the first page carries a load more form", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?kind=text")
		if status != http.StatusOK || !strings.Contains(body, `<form id="explore-more" class="load-more" action="/explore" method="get" get="/explore/more" auto="lazy" target="#explore-more(outerHTML)"`) || !strings.Contains(body, `<button type="submit">Load more</button>`) {
			t.Errorf("load more form: status %d", status)
		}
	})
	t.Run("the fragment appends the next matches and announces the count", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore/more?q=a&position=5&shown=5")
		if status != http.StatusOK || !strings.Contains(body, `<fragment target="#explore-more(outerHTML)"><article class="post">`) || !strings.Contains(body, `<fragment target="#explore-status(innerHTML)">5 more posts loaded, 10 total.</fragment>`) || strings.Contains(body, "<h1") {
			t.Errorf("load more fragment: status %d", status)
		}
	})
	t.Run("the full page fallback opens at the position", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q=a&position=5&shown=5")
		if status != http.StatusOK || !strings.Contains(body, `<h1 id="explore-title">Explore</h1>`) || !strings.Contains(body, `<input type="hidden" name="position" value="10"><input type="hidden" name="shown" value="10">`) {
			t.Errorf("full page fallback: status %d", status)
		}
	})
	t.Run("the end marker says what the sources returned", func(t *testing.T) {
		status, body := expGet(t, testClientHandler(t), "/explore?q=tomatoes")
		if status != http.StatusOK || !strings.Contains(body, "No more results from what your sources returned.") || strings.Contains(body, `id="explore-more"`) {
			t.Errorf("end marker: status %d", status)
		}
	})
	t.Run("a malformed position is 400", func(t *testing.T) {
		status, _ := expGet(t, testClientHandler(t), "/explore/more?position=%21%21%21")
		if status != http.StatusBadRequest {
			t.Errorf("malformed position: status %d", status)
		}
	})
}
