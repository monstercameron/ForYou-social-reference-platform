package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/names"
)

func TestDemoNameRecordsVerify(t *testing.T) {
	seen := map[string]string{}
	collision := false
	for _, r := range demoNameRecords() {
		if err := names.Verify(r, demoNamesIssuedAt.Add(time.Hour)); err != nil {
			t.Errorf("record for %q does not verify: %v", *r.DisplayName, err)
		}
		for id, name := range seen {
			collision = collision || (name == *r.DisplayName && id != r.AuthorID)
		}
		seen[r.AuthorID] = *r.DisplayName
	}
	if !collision {
		t.Error("the demo needs two keys that sign the same name")
	}
}

func TestAThirdOfDemoPostsAreSigned(t *testing.T) {
	signed := 0
	for _, p := range staticDemoPosts {
		if p.SignedName != "" {
			signed++
			if p.AuthorID == "" || p.Fingerprint != names.Fingerprint(p.AuthorID) {
				t.Errorf("post %q has a name without its key fingerprint", p.ID)
			}
		}
	}
	if share := signed * 100 / len(staticDemoPosts); share < 30 || share > 36 {
		t.Errorf("%d of %d posts are signed (%d%%); want about a third", signed, len(staticDemoPosts), share)
	}
}

func TestSignedAvatars(t *testing.T) {
	byKey := map[string]string{}
	for _, p := range staticDemoPosts {
		if p.Avatar != "" && p.SignedName == "" {
			t.Errorf("post %q has an avatar without a signed name", p.ID)
		}
		if p.AuthorID != "" {
			byKey[p.AuthorID] = p.SignedName + "|" + p.Avatar
		}
	}
	sameLook := map[string]int{}
	for _, look := range byKey {
		sameLook[look]++
	}
	if sameLook["Maya Chen|/static/demo-avatar-2.png"] != 2 {
		t.Error("the demo needs two keys that share a name and a picture, told apart only by fingerprint")
	}
	h := testClientHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/elliot", nil))
	if !strings.Contains(w.Body.String(), `<img class="identicon avatar" src="/static/demo-avatar-1.png" alt=""`) {
		t.Error("a signed post with a picture should show it in the avatar slot")
	}
	picture := httptest.NewRecorder()
	h.ServeHTTP(picture, httptest.NewRequest(http.MethodGet, "/static/demo-avatar-1.png", nil))
	if picture.Code != http.StatusOK || !strings.HasPrefix(picture.Body.String(), "\x89PNG") {
		t.Errorf("status %d; the bundled avatar should be served as a PNG", picture.Code)
	}
	plain := httptest.NewRecorder()
	h.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/posts/maya", nil))
	if strings.Contains(plain.Body.String(), `class="identicon avatar"`) {
		t.Error("an unsigned post keeps the key pattern")
	}
}

func TestFollowSignedName(t *testing.T) {
	h := testClientHandler(t)
	signed := staticPostsByID["elliot"]
	var sameKey, otherKey string
	for _, p := range staticDemoPosts {
		if p.ID != signed.ID && p.AuthorID == signed.AuthorID {
			sameKey = p.ID
		}
		if p.AuthorID != "" && p.AuthorID != signed.AuthorID {
			otherKey = p.ID
		}
	}
	w := postForm(h, "/follow/fragment", url.Values{"id": {signed.ID}, "follow": {"on"}})
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `target="#follow-`+signed.ID+`(outerHTML)"`) || !strings.Contains(body, `target="#follow-`+sameKey+`(outerHTML)"`) || strings.Contains(body, `#follow-`+otherKey+`(`) || strings.Count(body, `aria-pressed="true"`) != strings.Count(body, "<fragment") {
		t.Fatalf("status %d; following should update every post by that key and no other: %s", w.Code, body)
	}
	cookies := w.Result().Cookies()
	feed := func(path string, cookies ...*http.Cookie) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out.Body.String()
	}
	t.Run("following filter", func(t *testing.T) {
		following := feed("/feed?filter=following&limit=10000", cookies...)
		if !strings.Contains(following, `id="follow-`+signed.ID+`"`) || !strings.Contains(following, `id="follow-`+sameKey+`"`) || strings.Contains(following, `id="follow-`+otherKey+`"`) || strings.Contains(following, `id="thread-maya"`) {
			t.Error("the Following filter should list the followed key's posts only")
		}
	})
	t.Run("nobody followed", func(t *testing.T) {
		if !strings.Contains(feed("/feed?filter=following"), "No followed posts yet") {
			t.Error("an empty Following feed should explain how to follow")
		}
	})
	t.Run("unfollow", func(t *testing.T) {
		off := postForm(h, "/follow/fragment", url.Values{"id": {sameKey}, "follow": {"off"}}, cookies...)
		if off.Code != http.StatusOK || strings.Contains(off.Body.String(), `aria-pressed="true"`) {
			t.Errorf("status %d; unfollowing should clear the control", off.Code)
		}
		if following := feed("/feed?filter=following&limit=10000", cookies...); strings.Contains(following, `id="follow-`+signed.ID+`"`) {
			t.Error("an unfollowed key should leave the Following filter")
		}
	})
	t.Run("unsigned post", func(t *testing.T) {
		if w := postForm(h, "/follow/fragment", url.Values{"id": {"maya"}, "follow": {"on"}}); w.Code != http.StatusNotFound {
			t.Errorf("status %d; a post without a signed name has no key to follow", w.Code)
		}
	})
	t.Run("forged cookie", func(t *testing.T) {
		forged := &http.Cookie{Name: followCookie, Value: "not-a-key." + signed.AuthorID + "." + signed.AuthorID}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(forged)
		if got := followedAuthors(r); len(got) != 1 || got[0] != signed.AuthorID {
			t.Errorf("followed = %v; want the one valid key once", got)
		}
	})
}

func TestSingleImageOpensGallery(t *testing.T) {
	h := testClientHandler(t)
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code, w.Body.String()
	}
	if _, feed := get("/?filter=images&limit=10000"); !strings.Contains(feed, `<input type="hidden" name="id" value="travel"><input type="hidden" name="slide" value="1"><button type="submit" class="detail-gallery-open" aria-label="Open image larger">`) {
		t.Error("a post with one image should open it in the viewer")
	}
	code, viewer := get("/gallery?id=travel&slide=1")
	if code != http.StatusOK || !strings.Contains(viewer, "demo-landscape-3.svg") || strings.Contains(viewer, "Next image") || !strings.Contains(viewer, "quiet footpath along the harbor") || !strings.Contains(viewer, `href="/posts/travel"`) {
		t.Errorf("status %d; the viewer should show the one image with its caption and no slide controls", code)
	}
	if code, _ := get("/gallery?id=travel&slide=2"); code != http.StatusNotFound {
		t.Errorf("status %d; a single image has no second slide", code)
	}
}

func TestOwnPostsAreMarked(t *testing.T) {
	client := newTestClient(t, false)
	if _, err := client.app.keystore().Create(setTestProfile); err != nil {
		t.Fatal(err)
	}
	identity, found, err := client.app.keystore().Identity(setTestProfile)
	if err != nil || !found {
		t.Fatalf("identity: found %v, error %v", found, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: profileCookie, Value: setTestProfile})
	posts := []post{{ID: "mine", AuthorID: identity.DID}, {ID: "theirs", AuthorID: mydataTestDID(t)}}
	client.app.forReader(r, posts)
	if !posts[0].Own || posts[1].Own {
		t.Errorf("own = %v for the reader's post and %v for another key's; want true and false", posts[0].Own, posts[1].Own)
	}
}
