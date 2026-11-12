package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
)

var namesTestNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func namesTestClient(t *testing.T, config Config) *Client {
	t.Helper()
	config.DataDir = t.TempDir()
	config.PublicOrigin = "https://example.test"
	config.Now = func() time.Time { return namesTestNow }
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func namesTestRecord(t *testing.T, seed byte, name *string, issuedAt time.Time) names.NameRecord {
	t.Helper()
	key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	record, err := names.Build(key, name, nil, issuedAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func namesTestView(record names.NameRecord) sdk.NameView {
	return sdk.NameView{Record: record, Found: true, Withdrawn: record.DisplayName == nil, Fingerprint: names.Fingerprint(record.AuthorID)}
}

// namesTestStore builds a signed name record for seed and caches it as verified.
func namesTestStore(t *testing.T, client *Client, seed byte, name *string) names.NameRecord {
	t.Helper()
	record := namesTestRecord(t, seed, name, namesTestNow.Add(-time.Hour))
	client.app.nameCache().store(record.AuthorID, namesTestView(record), namesTestNow)
	return record
}

// namesTestLabel returns the label fragment of one key as the reader with cookies sees it.
func namesTestLabel(t *testing.T, client *Client, author string, cookies ...*http.Cookie) string {
	t.Helper()
	return getWith(client.Handler(), "/names/label?author="+url.QueryEscape(author), cookies...)
}

// namesTestProfile returns the profile cookie a response set.
func namesTestProfile(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == profileCookie {
			return cookie
		}
	}
	t.Fatal("no profile cookie was set")
	return nil
}

// namesTestProvider serves the given name records at the name read route and 404 for any other key.
func namesTestProvider(t *testing.T, requests *atomic.Int32, records ...names.NameRecord) *httptest.Server {
	t.Helper()
	served := map[string][]byte{}
	for _, record := range records {
		data, err := names.Encode(record)
		if err != nil {
			t.Fatal(err)
		}
		served["/v1/names/"+record.AuthorID] = data
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data, ok := served[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(provider.Close)
	return provider
}

func TestNamesLabel(t *testing.T) {
	client := namesTestClient(t, Config{})
	zoe := "Zoë Example"
	record := namesTestStore(t, client, 7, &zoe)
	t.Run("shows the verified name with the fingerprint, the neutral mark and a link to the key", func(t *testing.T) {
		label := namesTestLabel(t, client, record.AuthorID)
		if !strings.Contains(label, "<bdi>Zoë Example</bdi>") {
			t.Errorf("name is not isolated in bdi: %s", label)
		}
		if !strings.Contains(label, "<code>"+names.Fingerprint(record.AuthorID)+"</code>") {
			t.Error("fingerprint is missing")
		}
		if !strings.Contains(label, ", signed by this key") {
			t.Error("neutral signed mark is missing")
		}
		if !strings.Contains(label, `href="/authors/did%3Akey%3Az6Mk`) {
			t.Errorf("label does not link to the key's author page: %s", label)
		}
		if strings.Contains(label, "Name shared") {
			t.Error("a name nobody else uses is marked as shared")
		}
	})
	t.Run("an Arabic name stays inside bdi", func(t *testing.T) {
		arabic := "مرحبا"
		record := namesTestStore(t, client, 13, &arabic)
		if label := namesTestLabel(t, client, record.AuthorID); !strings.Contains(label, "<bdi>مرحبا</bdi>") {
			t.Error("Arabic name is not inside bdi")
		}
	})
	t.Run("a withdrawn name shows the key only", func(t *testing.T) {
		record := namesTestStore(t, client, 8, nil)
		label := namesTestLabel(t, client, record.AuthorID)
		if !strings.Contains(label, `<span class="signed-name">Key</span>`) || !strings.Contains(label, names.Fingerprint(record.AuthorID)) {
			t.Errorf("withdrawn label does not show the key and fingerprint: %s", label)
		}
		if strings.Contains(label, "<bdi>") || strings.Contains(label, "signed by this key") {
			t.Error("withdrawn label still shows a name")
		}
	})
	t.Run("a display name is escaped and never read as markup", func(t *testing.T) {
		hostile := "<img src=x onerror=alert(1)>"
		record := namesTestStore(t, client, 9, &hostile)
		label := namesTestLabel(t, client, record.AuthorID)
		if strings.Contains(label, "<img") {
			t.Error("name was rendered as markup")
		}
		if !strings.Contains(label, "<bdi>&lt;img src=x onerror=alert(1)&gt;</bdi>") {
			t.Errorf("name is not escaped inside bdi: %s", label)
		}
	})
	t.Run("two cached keys with matching names are both marked", func(t *testing.T) {
		ada, upper := "Ada", "ADA"
		first := namesTestStore(t, client, 10, &ada)
		second := namesTestStore(t, client, 11, &upper)
		if !strings.Contains(namesTestLabel(t, client, first.AuthorID), "<span>Name shared</span>") {
			t.Error("first key is not marked")
		}
		if !strings.Contains(namesTestLabel(t, client, second.AuthorID), "<span>Name shared</span>") {
			t.Error("second key is not marked")
		}
	})
	t.Run("a different name is not marked", func(t *testing.T) {
		bob := "Bob"
		record := namesTestStore(t, client, 12, &bob)
		if strings.Contains(namesTestLabel(t, client, record.AuthorID), "Name shared") {
			t.Error("unshared name is marked")
		}
	})
	t.Run("the label fragment is not stored and a bad key is a bad request", func(t *testing.T) {
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/names/label?author="+url.QueryEscape(record.AuthorID), nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `<div class="names-label-region"><a class="signed-label"`) {
			t.Errorf("label fragment = %d %q %q", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
		bad := httptest.NewRecorder()
		client.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/names/label?author=nope", nil))
		if bad.Code != http.StatusBadRequest {
			t.Errorf("bad key status = %d; want 400", bad.Code)
		}
	})
}

func TestNamesPetnames(t *testing.T) {
	client := namesTestClient(t, Config{})
	handler := client.Handler()
	zoe, ada := "Zoë Example", "Ada"
	record := namesTestStore(t, client, 20, &zoe)
	adaKey := namesTestStore(t, client, 21, &ada)
	otherKey := namesTestRecord(t, 22, nil, namesTestNow).AuthorID
	saved := postForm(handler, namesAuthorPath(record.AuthorID)+"/petname", url.Values{"petname": {"Mom"}})
	if saved.Code != http.StatusSeeOther || saved.Header().Get("Location") != namesAuthorPath(record.AuthorID)+"?petname=saved" {
		t.Fatalf("petname post = %d %q; want 303 to the author page", saved.Code, saved.Header().Get("Location"))
	}
	profile := namesTestProfile(t, saved)
	t.Run("a petname replaces the display name for this profile", func(t *testing.T) {
		mine := namesTestLabel(t, client, record.AuthorID, profile)
		if !strings.Contains(mine, "<bdi>Mom</bdi><span class=\"signed-own\">your name</span>") {
			t.Errorf("petname is not shown with the your name tag: %s", mine)
		}
		if strings.Contains(mine, "Zoë") {
			t.Error("display name is still shown beside the petname")
		}
	})
	t.Run("another profile still sees the display name", func(t *testing.T) {
		if other := namesTestLabel(t, client, record.AuthorID); !strings.Contains(other, "<bdi>Zoë Example</bdi>") {
			t.Error("another profile does not see the display name")
		}
	})
	t.Run("a petname given to another key marks the key whose name matches", func(t *testing.T) {
		before := namesTestLabel(t, client, adaKey.AuthorID, profile)
		if strings.Contains(before, "Name shared") {
			t.Fatal("Ada is marked before any petname matches it")
		}
		postForm(handler, namesAuthorPath(otherKey)+"/petname", url.Values{"petname": {"ada"}}, profile)
		if after := namesTestLabel(t, client, adaKey.AuthorID, profile); !strings.Contains(after, "<span>Name shared</span>") {
			t.Errorf("Ada is not marked after another key got the petname ada: %s", after)
		}
	})
	t.Run("a petname with a bidirectional control is rejected at its input", func(t *testing.T) {
		w := postForm(handler, namesAuthorPath(record.AuthorID)+"/petname/fragment", url.Values{"petname": {"Mom‮"}}, profile)
		body := w.Body.String()
		if !strings.Contains(body, `<p id="names-petname-error" class="names-error" role="alert">`) || !strings.Contains(body, `aria-describedby="names-petname-help names-petname-error"`) {
			t.Errorf("error is not tied to the petname input: %s", body)
		}
		if label := namesTestLabel(t, client, record.AuthorID, profile); !strings.Contains(label, "<bdi>Mom</bdi>") {
			t.Error("the rejected petname replaced the saved one")
		}
	})
	t.Run("clearing the petname shows the display name again", func(t *testing.T) {
		w := postForm(handler, namesAuthorPath(record.AuthorID)+"/petname/clear/fragment", url.Values{}, profile)
		if !strings.Contains(w.Body.String(), "Cleared.") {
			t.Errorf("clear fragment = %q", w.Body.String())
		}
		if label := namesTestLabel(t, client, record.AuthorID, profile); !strings.Contains(label, "<bdi>Zoë Example</bdi>") {
			t.Error("display name is not back after clearing")
		}
	})
}

func TestNamesDemoCards(t *testing.T) {
	client := namesTestClient(t, Config{})
	handler := client.Handler()
	records := demoNameRecords()
	maya := records[1].AuthorID
	t.Run("a demo name another demo key also signed is marked in the feed", func(t *testing.T) {
		body := getWith(handler, "/?limit=10000")
		if !strings.Contains(body, "<code>"+names.Fingerprint(maya)+"</code><span class=\"signed-shared\">") {
			t.Error("the Maya Chen label is not marked")
		}
		if !strings.Contains(body, "<code>"+names.Fingerprint(records[0].AuthorID)+"</code></span>") {
			t.Error("the Elliot Brooks label, whose name one key uses, is marked or missing")
		}
	})
	t.Run("the post page shows the label exactly once", func(t *testing.T) {
		body := getWith(handler, "/posts/art-studio")
		if got := strings.Count(body, `<a class="signed-label"`); got != 1 {
			t.Errorf("labels on the post page = %d; want 1", got)
		}
	})
	saved := postForm(handler, namesAuthorPath(maya)+"/petname", url.Values{"petname": {"Mom"}})
	profile := namesTestProfile(t, saved)
	t.Run("a petname shows on the reader's cards", func(t *testing.T) {
		body := getWith(handler, "/posts/art-studio", profile)
		if !strings.Contains(body, "<bdi>Mom</bdi><span class=\"signed-own\">your name</span>") {
			t.Error("petname is not shown on the post page")
		}
		if !strings.Contains(body, `aria-label="Follow Mom, fingerprint `+names.Fingerprint(maya)+`"`) {
			t.Error("follow control does not name the key as the label does")
		}
	})
	t.Run("an embed shows the signed name, never the reader's petname", func(t *testing.T) {
		body := getWith(handler, "/embed/art-studio", profile)
		if !strings.Contains(body, "<bdi>Maya Chen</bdi>") {
			t.Errorf("embed does not show the signed name: %s", body)
		}
		if strings.Contains(body, "Mom") {
			t.Error("embed shows the reader's petname")
		}
	})
}

func TestNamesRefresh(t *testing.T) {
	zoe := "Zoë Example"
	record := namesTestRecord(t, 30, &zoe, namesTestNow.Add(-time.Hour))
	service := namesTestRecord(t, 31, nil, namesTestNow).AuthorID
	var requests atomic.Int32
	provider := namesTestProvider(t, &requests, record)
	client := namesTestClient(t, Config{Dev: true, Providers: []Endpoint{{URL: provider.URL, ServiceID: service}}})
	t.Run("rendering a label contacts no provider", func(t *testing.T) {
		label := namesTestLabel(t, client, record.AuthorID)
		if !strings.Contains(label, `<span class="signed-name">Key</span>`) {
			t.Errorf("an unfetched key shows more than its fingerprint: %s", label)
		}
		if got := requests.Load(); got != 0 {
			t.Errorf("provider requests = %d; want 0", got)
		}
	})
	t.Run("one run of the refresh job fetches the wanted author", func(t *testing.T) {
		if err := refreshNames(t.Context(), client.app); err != nil {
			t.Fatal(err)
		}
		if got := requests.Load(); got != 1 {
			t.Fatalf("refresh provider requests = %d; want 1", got)
		}
		if label := namesTestLabel(t, client, record.AuthorID); !strings.Contains(label, "<bdi>Zoë Example</bdi>") {
			t.Error("next render does not show the fetched name")
		}
	})
	t.Run("a refresh that reaches no provider keeps the cached name", func(t *testing.T) {
		provider.Close()
		client.app.nameCache().store(record.AuthorID, namesTestView(record), namesTestNow.Add(-time.Hour))
		if err := refreshNames(t.Context(), client.app); err != nil {
			t.Fatal(err)
		}
		if label := namesTestLabel(t, client, record.AuthorID); !strings.Contains(label, "<bdi>Zoë Example</bdi>") {
			t.Error("a failed refresh removed the cached name")
		}
	})
}

func TestNamesAuthorPage(t *testing.T) {
	zoe := "Zoë Example"
	named := namesTestRecord(t, 40, &zoe, namesTestNow.Add(-2*time.Hour))
	key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{40}, 32))
	if err != nil {
		t.Fatal(err)
	}
	withdrawal, err := names.Build(key, nil, nil, namesTestNow.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := namesTestRecord(t, 41, nil, namesTestNow).AuthorID
	var requests atomic.Int32
	older := namesTestProvider(t, &requests, named)
	newer := namesTestProvider(t, &requests, withdrawal)
	client := namesTestClient(t, Config{Dev: true, Providers: []Endpoint{{URL: older.URL, ServiceID: service, Label: "First provider"}, {URL: newer.URL, ServiceID: service, Label: "Second provider"}}})
	handler := client.Handler()
	t.Run("a malformed key is not found and no provider is contacted", func(t *testing.T) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/authors/did%3Akey%3Anope", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("author page status = %d; want 404", w.Code)
		}
		if got := requests.Load(); got != 0 {
			t.Errorf("provider requests = %d; want 0", got)
		}
	})
	t.Run("the page and the panel fragment show the same panel", func(t *testing.T) {
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, namesAuthorPath(named.AuthorID), nil))
		panel := getWith(handler, namesAuthorPath(named.AuthorID)+"/panel")
		if page.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(page.Body.String(), "<title>Author name") {
			t.Errorf("author page headers or title are wrong: %q", page.Header().Get("Cache-Control"))
		}
		if !strings.Contains(page.Body.String(), `<h1 class="names-title"><span class="signed-name">Key</span>`) {
			t.Error("heading does not show the key of an unchecked author")
		}
		if !strings.Contains(panel, "Full key (did:key)") || !strings.Contains(panel, `value="`+named.AuthorID+`"`) || !strings.Contains(panel, `id="names-petname"`) {
			t.Errorf("panel fragment is missing the key or the petname form: %s", panel)
		}
		if !strings.Contains(page.Body.String(), `<section id="names-author-panel"`) || !strings.Contains(panel, `<section id="names-author-panel"`) {
			t.Error("the page and the fragment do not share the panel")
		}
		if got := requests.Load(); got != 0 {
			t.Errorf("opening the panel made %d provider requests; want 0", got)
		}
	})
	t.Run("checking the name shows the withdrawal as current and the first provider as older", func(t *testing.T) {
		body := getWith(handler, namesAuthorPath(named.AuthorID)+"/name")
		if !strings.Contains(body, "This key has withdrawn its name.") {
			t.Errorf("withdrawal is not current: %s", body)
		}
		if !strings.Contains(body, "<li>First provider: Older name</li>") || !strings.Contains(body, "<li>Second provider: Current name</li>") {
			t.Errorf("per-provider results are wrong: %s", body)
		}
	})
	t.Run("the full-page check shows the same result", func(t *testing.T) {
		body := getWith(handler, namesAuthorPath(named.AuthorID)+"?check=1")
		if !strings.Contains(body, "<li>First provider: Older name</li>") {
			t.Errorf("full-page check is missing the provider results: %s", body)
		}
	})
}
