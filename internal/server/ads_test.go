package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func voteCookie(id, vote string) *http.Cookie {
	return &http.Cookie{Name: feedbackCookieName(id), Value: vote + ".1"}
}

func getWith(h http.Handler, path string, cookies ...*http.Cookie) string {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Body.String()
}

func TestLikedFilter(t *testing.T) {
	h := testClientHandler(t)
	t.Run("lists liked posts only", func(t *testing.T) {
		body := getWith(h, "/feed?filter=liked&limit=10000", voteCookie("garden", "up"), voteCookie("music", "down"), voteCookie("ad-seeds", "up"))
		if !strings.Contains(body, `id="thread-garden"`) || !strings.Contains(body, `id="thread-ad-seeds"`) || strings.Contains(body, `id="thread-music"`) || strings.Contains(body, `id="thread-maya"`) {
			t.Error("the Liked filter should list the liked post and the liked ad, not the disliked or untouched ones")
		}
	})
	t.Run("nothing liked yet", func(t *testing.T) {
		if !strings.Contains(getWith(h, "/feed?filter=liked"), "No liked posts yet") {
			t.Error("an empty Liked view should say how to fill it")
		}
	})
	t.Run("both new pills are offered", func(t *testing.T) {
		body := getWith(h, "/")
		if !strings.Contains(body, `name="filter" value="liked"`) || !strings.Contains(body, `name="filter" value="ads"`) {
			t.Error("the feed should offer the Liked and Ads filters")
		}
	})
}

func TestAdsForReader(t *testing.T) {
	request := func(cookies ...*http.Cookie) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		return r
	}
	t.Run("without votes the order is the listed order and no reason is given", func(t *testing.T) {
		ads := adsFor(request())
		if len(ads) != len(demoAds) || ads[0].ID != "ad-coffee" || ads[0].Why != "" {
			t.Errorf("first ad = %q with reason %q of %d; want ad-coffee, no reason, all %d ads", ads[0].ID, ads[0].Why, len(ads), len(demoAds))
		}
	})
	t.Run("a liked topic brings its ad first and says why", func(t *testing.T) {
		ads := adsFor(request(voteCookie("garden", "up")))
		if ads[0].ID != "ad-seeds" || ads[0].Why != "Because you liked posts about Gardening" {
			t.Errorf("first ad = %q with reason %q; want ad-seeds because of Gardening", ads[0].ID, ads[0].Why)
		}
	})
	t.Run("two liked topics are both named", func(t *testing.T) {
		ads := adsFor(request(voteCookie("science", "up"), voteCookie("space", "up")))
		if ads[0].ID != "ad-telescope" || ads[0].Why != "Because you liked posts about Science and Space" {
			t.Errorf("first ad = %q with reason %q; want ad-telescope because of Science and Space", ads[0].ID, ads[0].Why)
		}
	})
	t.Run("a disliked topic sends its ad to the end", func(t *testing.T) {
		ads := adsFor(request(voteCookie("pets", "down")))
		if last := ads[len(ads)-1]; last.ID != "ad-petbed" || last.Why != "" {
			t.Errorf("last ad = %q; want ad-petbed", last.ID)
		}
	})
	t.Run("a disliked ad is removed", func(t *testing.T) {
		ads := adsFor(request(voteCookie("ad-coffee", "down")))
		if len(ads) != len(demoAds)-1 || ads[0].ID == "ad-coffee" {
			t.Errorf("%d ads, first %q; want ad-coffee gone", len(ads), ads[0].ID)
		}
	})
}

func TestAdsSection(t *testing.T) {
	h := testClientHandler(t)
	t.Run("section lists ads with the note and the reason", func(t *testing.T) {
		body := getWith(h, "/feed?filter=ads&limit=10000", voteCookie("garden", "up"))
		first := strings.Index(body, `id="thread-ad-seeds"`)
		if first < 0 || first > strings.Index(body, `id="thread-ad-coffee"`) || !strings.Contains(body, "Nothing about you is sent to an advertiser.") || !strings.Contains(body, `<span class="post-why">Because you liked posts about Gardening</span>`) || strings.Contains(body, `id="thread-maya"`) {
			t.Error("the ads section should list only ads, the relevant one first with its reason, under the privacy note")
		}
	})
	t.Run("an ad from the section can be opened", func(t *testing.T) {
		if !strings.Contains(getWith(h, "/posts/ad-telescope"), "Saturn") {
			t.Error("an ad outside the feed should have a post page")
		}
	})
	t.Run("right bar shows the two most relevant", func(t *testing.T) {
		body := getWith(h, "/", voteCookie("pets", "up"))
		panel := body[strings.Index(body, `class="component-card ads-panel"`):]
		if strings.Count(panel[:strings.Index(panel, "</section>")], "<li>") != 2 || !strings.Contains(panel, `href="/posts/ad-petbed"`) || !strings.Contains(panel, "Because you liked posts about Animals") {
			t.Error("the right bar should show two ads, the pet bed first with its reason")
		}
	})
	t.Run("ads stay out of the ordinary feed order", func(t *testing.T) {
		if strings.Contains(getWith(h, "/feed?filter=all&limit=10000", voteCookie("pets", "up")), `id="thread-ad-petbed"`) {
			t.Error("an ad that lives in the ads section must not be pushed into the feed")
		}
	})
}
