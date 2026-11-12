package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// followingFakeSource answers author lookups from fixed posts, fails them all,
// or answers with a source that did not answer.
type followingFakeSource struct {
	*demoRecordSource
	posts      map[string][]post
	failing    bool
	unanswered bool
}

func (f *followingFakeSource) ByAuthor(_ context.Context, _ *http.Request, authorID string) (recordPage, error) {
	if f.failing {
		return recordPage{}, errors.New("provider unavailable")
	}
	if f.unanswered {
		return recordPage{Coverage: Coverage{Sources: []SourceStatus{
			{Label: "Fixture provider", Endpoint: "http://provider.invalid", OK: false, Text: "did not answer"},
			{Label: "Local copy", OK: true, Text: "0 more held records match"},
		}}}, nil
	}
	return recordPage{Posts: f.posts[authorID], Coverage: Coverage{Sources: []SourceStatus{{Label: "Fixture provider", Endpoint: "http://provider.invalid", OK: true, Text: "answered"}}}}, nil
}

// followingFixturePost is a text post by author, written out by its message ID and timestamp.
func followingFixturePost(id, author, timestamp string) post {
	return post{ID: id, AuthorID: author, Timestamp: timestamp, Text: "Post " + id[1:], Kind: "text"}
}

func followingDo(h http.Handler, path string, form url.Values, fragment bool, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method = http.MethodPost
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if fragment {
		r.Header.Set("Sec-Fetch-Dest", "empty")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func followingTestKey(t *testing.T, seed byte) string {
	t.Helper()
	priv, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys.DIDOf(priv)
}

// followingCookie returns the named cookie of a response, failing the test when it is absent.
func followingCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	i := slices.IndexFunc(cookies, func(c *http.Cookie) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no %s cookie in the response", name)
	}
	return cookies[i]
}

func followingProfileID(t *testing.T, cookies []*http.Cookie) string {
	t.Helper()
	return followingCookie(t, cookies, profileCookie).Value
}

func followingStateOf(t *testing.T, client *Client, profile string) followingState {
	t.Helper()
	state, err := client.app.followingRead(profile)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// followingFollow follows did from the browser holding cookies, and returns the browser's cookies.
func followingFollow(t *testing.T, h http.Handler, did string, cookies ...*http.Cookie) []*http.Cookie {
	t.Helper()
	w := followingDo(h, "/following/follow", url.Values{"author": {did}, "via": {"author"}}, true, cookies...)
	if w.Code != http.StatusOK {
		t.Fatalf("following %s returned %d", did, w.Code)
	}
	return append(cookies, w.Result().Cookies()...)
}

func TestFollowingControl(t *testing.T) {
	client := newTestClient(t, false)
	h := client.Handler()
	author := followingTestKey(t, 1)
	fingerprintID := followingFingerprintID(names.Fingerprint(author))
	var cookies []*http.Cookie

	t.Run("fragment follow answers the toggle and records the key", func(t *testing.T) {
		w := followingDo(h, "/following/follow", url.Values{"author": {author}, "via": {"author"}}, true)
		cookies = w.Result().Cookies()
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="following-control-`+fingerprintID+`"`) || !strings.Contains(body, `aria-pressed="true"`) || !strings.Contains(body, "Following this key.") {
			t.Fatalf("status %d; the fragment should show the followed toggle: %s", w.Code, body)
		}
		state := followingStateOf(t, client, followingProfileID(t, cookies))
		if len(state.Follows) != 1 || state.Follows[0].AuthorID != author || state.Follows[0].Via != "author" {
			t.Errorf("follows = %+v; want the author once, via author", state.Follows)
		}
	})

	t.Run("full page twin shows the control with the full key", func(t *testing.T) {
		w := followingDo(h, "/following/control?author="+url.QueryEscape(author), nil, false, cookies...)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<h1>Follow a key</h1>") || !strings.Contains(body, author) || !strings.Contains(body, `<span class="following-fp">`+names.Fingerprint(author)+`</span>`) || !strings.Contains(body, `aria-pressed="true"`) {
			t.Errorf("status %d; the control page should show the key and its state", w.Code)
		}
	})

	t.Run("a cross-origin post is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/following/unfollow", strings.NewReader(url.Values{"author": {author}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("status %d; a cross-site unfollow should be 403", w.Code)
		}
	})

	t.Run("a malformed key is refused", func(t *testing.T) {
		if w := followingDo(h, "/following/follow", url.Values{"author": {"did:key:zBad"}}, true, cookies...); w.Code != http.StatusBadRequest {
			t.Errorf("status %d; a malformed key should be 400", w.Code)
		}
		if w := followingDo(h, "/following/control?author=did:key:zBad", nil, true); w.Code != http.StatusBadRequest {
			t.Errorf("status %d; a malformed control key should be 400", w.Code)
		}
	})

	t.Run("unfollow removes the key", func(t *testing.T) {
		w := followingDo(h, "/following/unfollow", url.Values{"author": {author}, "via": {"author"}}, true, cookies...)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `aria-pressed="false"`) || !strings.Contains(w.Body.String(), "No longer following this key.") {
			t.Errorf("status %d; unfollow should release the toggle", w.Code)
		}
		if state := followingStateOf(t, client, followingProfileID(t, cookies)); len(state.Follows) != 0 {
			t.Errorf("follows = %+v; want none after unfollow", state.Follows)
		}
	})

	t.Run("a full page unfollow from the keys list returns to the list", func(t *testing.T) {
		w := followingDo(h, "/following/unfollow", url.Values{"author": {author}, "via": {"manual"}, "view": {"row"}}, false, cookies...)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/following/keys" {
			t.Errorf("status %d to %q; want 303 to /following/keys", w.Code, w.Header().Get("Location"))
		}
	})

	t.Run("another profile has no follows", func(t *testing.T) {
		w := followingDo(h, "/following/keys", nil, false)
		if !strings.Contains(w.Body.String(), "No keys followed yet") {
			t.Error("a new browser should see no followed keys")
		}
	})

	t.Run("the legacy cookie migrates and expires", func(t *testing.T) {
		legacy := &http.Cookie{Name: followCookie, Value: author}
		w := followingDo(h, "/following/keys", nil, false, legacy)
		migrated := slices.ContainsFunc(w.Result().Cookies(), func(c *http.Cookie) bool { return c.Name == followCookie && c.MaxAge < 0 })
		if !migrated || !strings.Contains(w.Body.String(), `id="following-key-`+fingerprintID+`"`) {
			t.Error("a legacy follow cookie should move into the profile and expire")
		}
	})
}

func TestFollowingStore(t *testing.T) {
	author := followingTestKey(t, 1)
	other := followingTestKey(t, 2)

	t.Run("a full list refuses a new key", func(t *testing.T) {
		state := followingState{Follows: make([]followingEntry, followingKeyLimit)}
		if err := followingAdd(&state, other, followingViaAuthor, followingTestTime); !errors.Is(err, followingErrLimit) {
			t.Errorf("error = %v; want the limit message", err)
		}
	})

	t.Run("a followed key is not added twice", func(t *testing.T) {
		state := followingState{}
		if err := followingAdd(&state, author, followingViaPost, followingTestTime); err != nil {
			t.Fatal(err)
		}
		if err := followingAdd(&state, author, followingViaAuthor, followingTestTime); err != nil || len(state.Follows) != 1 {
			t.Errorf("follows = %+v; want the key once", state.Follows)
		}
	})
}

func TestFollowingFeed(t *testing.T) {
	client := newTestClient(t, false)
	h := client.Handler()
	first, second, third := followingTestKey(t, 3), followingTestKey(t, 4), followingTestKey(t, 5)
	source := &followingFakeSource{demoRecordSource: &demoRecordSource{app: client.app}, posts: map[string][]post{
		first: {
			followingFixturePost("m00", first, "2026-03-01T10:00:00Z"),
			followingFixturePost("m03", first, "2026-03-01T10:03:00Z"),
			followingFixturePost("m06", first, "2026-03-01T10:06:00Z"),
			followingFixturePost("m09", first, "2026-03-01T10:09:00Z"),
			followingFixturePost("m12", first, "2026-03-01T10:12:00Z"),
			followingFixturePost("m15", first, "2026-03-01T10:15:00Z"),
			followingFixturePost("m18", first, "2026-03-01T10:18:00Z"),
			followingFixturePost("m21", first, "2026-03-01T10:21:00Z"),
			followingFixturePost("m24", first, "2026-03-01T10:24:00Z"),
		},
		second: {
			followingFixturePost("m01", second, "2026-03-01T10:01:00Z"),
			followingFixturePost("m04", second, "2026-03-01T10:04:00Z"),
			followingFixturePost("m07", second, "2026-03-01T10:07:00Z"),
			followingFixturePost("m10", second, "2026-03-01T10:10:00Z"),
			followingFixturePost("m13", second, "2026-03-01T10:13:00Z"),
			followingFixturePost("m16", second, "2026-03-01T10:16:00Z"),
			followingFixturePost("m19", second, "2026-03-01T10:19:00Z"),
			followingFixturePost("m22", second, "2026-03-01T10:22:00Z"),
		},
		third: {
			followingFixturePost("m02", third, "2026-03-01T10:02:00Z"),
			followingFixturePost("m05", third, "2026-03-01T10:05:00Z"),
			followingFixturePost("m08", third, "2026-03-01T10:08:00Z"),
			followingFixturePost("m11", third, "2026-03-01T10:11:00Z"),
			followingFixturePost("m14", third, "2026-03-01T10:14:00Z"),
			followingFixturePost("m17", third, "2026-03-01T10:17:00Z"),
			followingFixturePost("m20", third, "2026-03-01T10:20:00Z"),
			followingFixturePost("m23", third, "2026-03-01T10:23:00Z"),
		},
	}}
	client.app.recordSource = source
	cookies := followingFollow(t, h, first)
	cookies = followingFollow(t, h, second, cookies...)
	cookies = followingFollow(t, h, third, cookies...)
	position := func(body, id string) int { return strings.Index(body, `id="feedback-`+id+`"`) }

	t.Run("posts from every key merge newest first without duplicates", func(t *testing.T) {
		body := followingDo(h, "/following", nil, false, cookies...).Body.String()
		newest, next := position(body, "m24"), position(body, "m23")
		if newest < 0 || next < 0 || newest > next {
			t.Fatal("the newest post should come first")
		}
		if position(body, "m05") < 0 || position(body, "m04") >= 0 {
			t.Error("the first page should hold twenty posts, ending at m05")
		}
		if !strings.Contains(body, "Some posts may be missing.") || !strings.Contains(body, `name="cursor" value="`) {
			t.Error("the page should carry the coverage note and a Load more cursor")
		}
	})

	t.Run("load more returns the rest in order", func(t *testing.T) {
		page := followingDo(h, "/following", nil, false, cookies...).Body.String()
		_, after, _ := strings.Cut(page, `name="cursor" value="`)
		token, _, _ := strings.Cut(after, `"`)
		body := followingDo(h, "/following/more?cursor="+url.QueryEscape(token), nil, true, cookies...).Body.String()
		if position(body, "m04") < 0 || position(body, "m00") < 0 || position(body, "m04") > position(body, "m00") || position(body, "m05") >= 0 {
			t.Errorf("the next batch should be m04 to m00 in order: %s", body)
		}
		if strings.Contains(body, `name="cursor"`) {
			t.Error("nothing is left after the last batch, so no cursor should remain")
		}
	})

	t.Run("an unknown cursor has expired", func(t *testing.T) {
		if body := followingDo(h, "/following/more?cursor=unknown", nil, true, cookies...).Body.String(); !strings.Contains(body, "This list has expired.") {
			t.Error("an unknown cursor should say the list has expired")
		}
	})

	t.Run("a failing source shows the error state with a retry", func(t *testing.T) {
		source.failing = true
		defer func() { source.failing = false }()
		body := followingDo(h, "/following", nil, false, cookies...).Body.String()
		if !strings.Contains(body, "Could not reach your sources.") || !strings.Contains(body, "Retry") {
			t.Error("all sources failing should show the error state with a retry")
		}
	})

	t.Run("no follows explains how to follow", func(t *testing.T) {
		body := followingDo(h, "/following", nil, false).Body.String()
		if !strings.Contains(body, "You do not follow any keys yet. Open an author or a post and choose Follow.") {
			t.Error("an empty Following feed should explain how to follow")
		}
	})

	t.Run("keys with no posts say so", func(t *testing.T) {
		source.posts = map[string][]post{}
		defer func() { source.posts = map[string][]post{} }()
		body := followingDo(h, "/following", nil, false, cookies...).Body.String()
		if !strings.Contains(body, "No posts from the keys you follow in your sources.") {
			t.Error("followed keys without posts should say so")
		}
	})

	t.Run("the keys list links each key's label and author", func(t *testing.T) {
		body := followingDo(h, "/following/keys", nil, false, cookies...).Body.String()
		label := "/names/label?author=" + url.QueryEscape(first)
		if !strings.Contains(body, `get="`+label+`"`) || !strings.Contains(body, "Open author") || !strings.Contains(body, "Followed ") {
			t.Errorf("each row should carry its label region and an author link: %s", body)
		}
	})

	t.Run("sources that do not answer show the error state", func(t *testing.T) {
		source.unanswered = true
		defer func() { source.unanswered = false }()
		body := followingDo(h, "/following", nil, false, cookies...).Body.String()
		if !strings.Contains(body, "Could not reach your sources.") {
			t.Error("keys whose sources did not answer should show the error state, not an empty feed")
		}
	})

	t.Run("a post the sources repeat shows once", func(t *testing.T) {
		original := source.posts[first]
		source.posts[first] = append(slices.Clone(original), followingFixturePost("m24", first, "2026-03-01T10:24:00Z"))
		defer func() { source.posts[first] = original }()
		body := followingDo(h, "/following", nil, false, cookies...).Body.String()
		if n := strings.Count(body, `id="feedback-m24"`); n != 1 {
			t.Errorf("m24 appears %d times; want once", n)
		}
	})
}

func TestFollowingNavigation(t *testing.T) {
	client := newTestClient(t, false)
	h := client.Handler()
	author := staticPostsByID["elliot"].AuthorID

	t.Run("the Following item links from the shell", func(t *testing.T) {
		if body := getWith(h, "/"); !strings.Contains(body, `href="/following"`) {
			t.Error("Home should link to Following")
		}
	})

	t.Run("the Following page marks its item current", func(t *testing.T) {
		if body := getWith(h, "/following"); !strings.Contains(body, `href="/following" aria-current="page"`) {
			t.Error("the Following page should mark its navigation item")
		}
	})

	t.Run("a signed post carries the author control", func(t *testing.T) {
		body := getWith(h, "/posts/elliot")
		control := "/following/control?author=" + url.QueryEscape(author)
		if !strings.Contains(body, `id="following-post-control"`) || !strings.Contains(body, `href="`+control+`"`) {
			t.Error("the post page should load the follow control for its author")
		}
	})
}

func TestFollowingSuccessions(t *testing.T) {
	client := newTestClient(t, false)
	h := client.Handler()
	oldSeed := bytes.Repeat([]byte{6}, 32)
	oldKey, err := keys.NewKeyFromSeed(oldSeed)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	oldID, newID := keys.DIDOf(oldKey), keys.DIDOf(newKey)
	link, err := succession.New(oldKey, newKey, followingTestTime)
	if err != nil {
		t.Fatal(err)
	}
	rival, err := succession.New(oldKey, otherKey, followingTestTime)
	if err != nil {
		t.Fatal(err)
	}
	cookies := followingFollow(t, h, oldID)
	profile := followingProfileID(t, cookies)
	holds := func(held ...wire.KeySuccession) {
		followingChecks.Lock()
		followingChecks.byKey[followingCheckKey(profile, oldID)] = followingCheck{at: client.app.now(), held: held}
		followingChecks.Unlock()
	}

	t.Run("a link offers the new key without following it", func(t *testing.T) {
		holds(link)
		body := followingDo(h, "/following/successions", nil, true, cookies...).Body.String()
		if !strings.Contains(body, "A key you follow named a new key") || !strings.Contains(body, "The old key signed this link.") || !strings.Contains(body, "Follow the new key") {
			t.Fatalf("the offer should show the linked key: %s", body)
		}
		if state := followingStateOf(t, client, profile); len(state.Follows) != 1 || state.Follows[0].AuthorID != oldID {
			t.Errorf("follows = %+v; the new key must not be followed automatically", state.Follows)
		}
	})

	t.Run("following the new key keeps the old one", func(t *testing.T) {
		holds(link)
		body := followingDo(h, "/following/follow", url.Values{"author": {newID}, "via": {"succession"}, "view": {"offers"}}, true, cookies...).Body.String()
		if !strings.Contains(body, "Unfollow the old key") {
			t.Errorf("after following the new key the offer should offer to unfollow the old one: %s", body)
		}
		state := followingStateOf(t, client, profile)
		if len(state.Follows) != 2 || !followingFollowed(state, oldID) || !followingFollowed(state, newID) {
			t.Errorf("follows = %+v; both keys should be followed", state.Follows)
		}
	})

	t.Run("dismissing hides the offer", func(t *testing.T) {
		holds(link)
		followingDo(h, "/following/unfollow", url.Values{"author": {newID}, "via": {"manual"}}, true, cookies...)
		body := followingDo(h, "/following/successions/dismiss", url.Values{"old": {oldID}, "new": {newID}}, true, cookies...).Body.String()
		if strings.Contains(body, "A key you follow named a new key") {
			t.Error("a dismissed offer should not come back")
		}
	})

	t.Run("conflicting links offer neither key", func(t *testing.T) {
		holds(link, rival)
		body := followingDo(h, "/following/successions", nil, true, cookies...).Body.String()
		if !strings.Contains(body, "This key names more than one new key. Neither is followed.") || strings.Contains(body, "Follow the new key") {
			t.Errorf("conflicting successions should show the conflict and no follow action: %s", body)
		}
		if !strings.Contains(body, names.Fingerprint(newID)) {
			t.Error("the conflict should list each candidate's fingerprint")
		}
	})

	t.Run("a full page follow from an offer returns to Following", func(t *testing.T) {
		holds(link)
		w := followingDo(h, "/following/follow", url.Values{"author": {newID}, "via": {"succession"}, "view": {"offers"}}, false, cookies...)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/following" {
			t.Errorf("status %d to %q; want 303 to /following", w.Code, w.Header().Get("Location"))
		}
	})

	t.Run("a browser without a profile gets an empty region", func(t *testing.T) {
		w := followingDo(h, "/following/successions", nil, true)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "could not be read") {
			t.Errorf("status %d: %s; want an empty region", w.Code, w.Body.String())
		}
	})
}

// TestFollowingProviderSuccessions checks successions that a provider serves for a followed key.
func TestFollowingProviderSuccessions(t *testing.T) {
	const oldID = "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"
	const newID = "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"
	const vector = `{"successions":[{"succession_id":"fyks2:a3156461bf70d3de6fa794f8f1d1cb6a0c7ac71674aa717c26004612b0c808b6","old_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","new_id":"did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG","effective_at":"2026-12-01T00:00:00Z","old_signature":"r_Zxt-fw5D9i8N9UgSaYi1FBeRALA9FbfZQF6MnO2nnX8kf8cBBT0jcu-1LhC4LUCbc46yQIyaEGL5Yqk1ZuDw","new_signature":"0JyWdFLhAHFOQKimHD13oKxTVvbW45tm3YR43wo94KM1FihVlMDGUarOv-3mk0ZLFWVz1qyD3ViRB5yb06S4BQ"}]}`
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/successions/"+oldID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(vector))
	}))
	defer provider.Close()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	client, err := NewClient(Config{Dev: true, DataDir: t.TempDir(), Now: func() time.Time { return now }, Providers: []Endpoint{{URL: provider.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Fixture provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	h := client.Handler()
	cookies := followingFollow(t, h, oldID)
	profile := followingProfileID(t, cookies)

	t.Run("a link the provider serves is offered and nothing is followed", func(t *testing.T) {
		body := followingDo(h, "/following/successions", nil, true, cookies...).Body.String()
		if !strings.Contains(body, "The old key signed this link.") || !strings.Contains(body, "Follow the new key") {
			t.Fatalf("the provider's link should be offered: %s", body)
		}
		if state := followingStateOf(t, client, profile); len(state.Follows) != 1 || state.Follows[0].AuthorID != oldID {
			t.Errorf("follows = %+v; the new key must not be followed automatically", state.Follows)
		}
	})

	t.Run("a checked key is asked again only after an hour", func(t *testing.T) {
		before := requests.Load()
		followingDo(h, "/following/successions", nil, true, cookies...)
		if got := requests.Load(); got != before {
			t.Fatalf("provider requests = %d; want no new request inside the hour", got)
		}
		now = now.Add(time.Hour)
		followingDo(h, "/following/successions", nil, true, cookies...)
		if got := requests.Load(); got != before+1 {
			t.Errorf("provider requests = %d; want one new request after the hour", got)
		}
	})

	t.Run("the new key is not followed by the provider's answer", func(t *testing.T) {
		if followingFollowed(followingStateOf(t, client, profile), newID) {
			t.Error("the provider's link must not add the new key to the follows")
		}
	})
}

// followingTestTime is a fixed effective date for the successions in these tests.
var followingTestTime = followingMustTime()

func followingMustTime() time.Time {
	when, err := wire.ParseTime("2026-12-01T00:00:00Z")
	if err != nil {
		panic(err)
	}
	return when
}
