package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"foryou.local/foryou/internal/rank"
)

func TestDimensionLevels(t *testing.T) {
	level := func(key string, f rank.Features) string {
		for _, d := range dimensions {
			if d.Key == key {
				return d.Levels[d.level(f)]
			}
		}
		return "unknown dimension"
	}
	want := func(t *testing.T, key string, f rank.Features, name string) {
		t.Helper()
		if got := level(key, f); got != name {
			t.Errorf("%s level = %q; want %q", key, got, name)
		}
	}
	t.Run("clean post is none everywhere", func(t *testing.T) {
		clean := rank.Features{Profanity: &rank.Profanity{}}
		want(t, "profanity", clean, "None")
		want(t, "threats", clean, "None")
		want(t, "hate", clean, "None")
		want(t, "harassment", clean, "None")
		want(t, "spam", clean, "None")
		want(t, "promotion", clean, "None")
		want(t, "adult", clean, "None")
		want(t, "secrets", clean, "None")
	})
	t.Run("profanity not rated for another language", func(t *testing.T) { want(t, "profanity", rank.Features{}, "None") })
	t.Run("mild profanity", func(t *testing.T) { want(t, "profanity", rank.Features{Profanity: &rank.Profanity{Mild: 2}}, "Mild") })
	t.Run("one strong word", func(t *testing.T) {
		want(t, "profanity", rank.Features{Profanity: &rank.Profanity{Mild: 3, Strong: 1}}, "Strong")
	})
	t.Run("two strong words", func(t *testing.T) {
		want(t, "profanity", rank.Features{Profanity: &rank.Profanity{Strong: 2}}, "Heavy")
	})
	t.Run("indirect threat", func(t *testing.T) { want(t, "threats", rank.Features{Threat: 0.5}, "Indirect") })
	t.Run("direct threat", func(t *testing.T) { want(t, "threats", rank.Features{Threat: 1}, "Direct") })
	t.Run("slur", func(t *testing.T) { want(t, "hate", rank.Features{Hate: rank.Hate{Slurs: 1}}, "Slur") })
	t.Run("directed hate outranks a slur", func(t *testing.T) {
		want(t, "hate", rank.Features{Hate: rank.Hate{Slurs: 2, Directed: true}}, "Directed")
	})
	t.Run("dehumanizing outranks directed", func(t *testing.T) {
		want(t, "hate", rank.Features{Hate: rank.Hate{Directed: true, Dehumanizing: true}}, "Dehumanizing")
	})
	t.Run("harassment below the floor", func(t *testing.T) { want(t, "harassment", rank.Features{Harass: 0.25}, "Mild") })
	t.Run("harassment at the floor", func(t *testing.T) { want(t, "harassment", rank.Features{Harass: 0.5}, "Strong") })
	t.Run("harassment at the top", func(t *testing.T) { want(t, "harassment", rank.Features{Harass: 1}, "Severe") })
	t.Run("shouting", func(t *testing.T) { want(t, "spam", rank.Features{CapsRatio: 0.7}, "Loud") })
	t.Run("repetition", func(t *testing.T) {
		want(t, "spam", rank.Features{Repetition: rank.Repetition{Rep3: 0.4}}, "Repetitive")
	})
	t.Run("shouted repetition", func(t *testing.T) {
		want(t, "spam", rank.Features{CapsRatio: 0.7, Repetition: rank.Repetition{CharRepeat: 0.5}}, "Extreme")
	})
	t.Run("one promotional phrase", func(t *testing.T) { want(t, "promotion", rank.Features{PromoHits: 1}, "Promotional") })
	t.Run("three promotional phrases", func(t *testing.T) { want(t, "promotion", rank.Features{PromoHits: 3}, "Pushy") })
	t.Run("asking for a recovery phrase outranks the rest", func(t *testing.T) {
		want(t, "promotion", rank.Features{PromoHits: 1, SolicitSecret: true}, "Asks for secrets")
	})
	t.Run("adult label", func(t *testing.T) { want(t, "adult", rank.Features{AdultLabel: true}, "Labelled adult") })
	t.Run("secret", func(t *testing.T) {
		want(t, "secrets", rank.Features{Secrets: []rank.SecretMatch{{Kind: "assigned_secret", Text: "x"}}}, "Credential-shaped")
	})
}

func TestNoDimensionHasMoreThanFourLevels(t *testing.T) {
	if len(dimensions) != 8 {
		t.Fatalf("%d dimensions; want 8", len(dimensions))
	}
	checked := func(t *testing.T, d dimension) {
		t.Helper()
		if len(d.Levels) < 2 || len(d.Levels) > 4 || d.Levels[0] != "None" || d.Shown < 0 || d.Shown >= len(d.Levels) {
			t.Errorf("dimension %q has levels %v and default %d; want two to four levels starting at None and a default among them", d.Key, d.Levels, d.Shown)
		}
	}
	checked(t, dimensions[0])
	checked(t, dimensions[1])
	checked(t, dimensions[2])
	checked(t, dimensions[3])
	checked(t, dimensions[4])
	checked(t, dimensions[5])
	checked(t, dimensions[6])
	checked(t, dimensions[7])
}

func TestRatingOnPostsAndReplies(t *testing.T) {
	h := testClientHandler(t)
	showAll := &http.Cookie{Name: filterCookie, Value: "32333311"}
	t.Run("a clean post says so", func(t *testing.T) {
		body := getWith(h, "/posts/maya")
		if !strings.Contains(body, `<span class="rating-chip severity-0">Nothing flagged</span>`) || strings.Contains(body, `class="filtered"`) {
			t.Error("a clean post should show an empty rating and not be folded")
		}
	})
	t.Run("the rating lists every dimension", func(t *testing.T) {
		body := getWith(h, "/posts/maya")
		if strings.Count(body[strings.Index(body, `class="rating-table"`):strings.Index(body, `class="rating-note"`)], "<dt>") != 10 {
			t.Error("the rating table should hold eight dimensions plus language and length")
		}
	})
	t.Run("an adult-labelled post is folded by default with its reason", func(t *testing.T) {
		body := getWith(h, "/posts/adult-1")
		if !strings.Contains(body, "Folded by your filters: Adult (Labelled adult)") || !strings.Contains(body, `Adult: Labelled adult</span>`) {
			t.Error("the adult post should be folded and name the dimension and level")
		}
	})
	t.Run("the same post is open when the reader allows it", func(t *testing.T) {
		body := getWith(h, "/posts/adult-1", showAll)
		if strings.Contains(body, "Folded by your filters: Adult") || !strings.Contains(body, `Adult: Labelled adult</span>`) {
			t.Error("with the filter raised the post is not folded but still carries its rating")
		}
	})
	t.Run("a reply with a credential is folded and its text can still be opened", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=tech-video&page=2")
		if strings.Count(body, "Folded by your filters: Secrets (Credential-shaped)") != 2 || !strings.Contains(body, "AWS_SECRET_ACCESS_KEY=") {
			t.Error("the two replies with credentials should be folded, with the text still present to open")
		}
	})
	t.Run("profanity is shown by default and still rated", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=sports&page=2")
		if !strings.Contains(body, `Profanity: Strong</span>`) || strings.Contains(body, "Folded by your filters: Profanity") {
			t.Error("a reply with one strong word should carry a Strong chip and stay open under the default filters")
		}
	})
	t.Run("a stricter choice folds it", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=sports&page=2", &http.Cookie{Name: filterCookie, Value: "00012200"})
		if !strings.Contains(body, "Folded by your filters: Profanity (Strong)") {
			t.Error("with profanity set to fold any, the reply should be folded")
		}
	})
	t.Run("the feed carries ratings too", func(t *testing.T) {
		body := getWith(h, "/feed?filter=all&limit=10000")
		if strings.Count(body, `<details class="post-rating">`) != len(staticDemoPosts) || !strings.Contains(body, "Folded by your filters: Promotion (Asks for secrets)") {
			t.Error("every post in the feed should carry a rating, and the prize scam should be folded")
		}
	})
}

func TestFilterSettings(t *testing.T) {
	h := testClientHandler(t)
	t.Run("settings offer every dimension with its default chosen", func(t *testing.T) {
		body := getWith(h, "/settings")
		if strings.Count(body, `<fieldset class="filter-row">`) != 8 || strings.Count(body, `type="radio"`) != 27 || !strings.Contains(body, `name="profanity" value="3" checked`) || !strings.Contains(body, `name="threats" value="0" checked`) || strings.Count(body, " checked>") != 8 {
			t.Error("the settings page should list eight dimensions, 27 choices, and mark one default each")
		}
	})
	t.Run("a saved cookie is shown as chosen", func(t *testing.T) {
		body := getWith(h, "/settings", &http.Cookie{Name: filterCookie, Value: "00012200"})
		if !strings.Contains(body, `name="profanity" value="0" checked`) {
			t.Error("the saved profanity choice should be selected")
		}
	})
	t.Run("a damaged cookie falls back to the defaults", func(t *testing.T) {
		body := getWith(h, "/settings", &http.Cookie{Name: filterCookie, Value: "39012200"})
		if !strings.Contains(body, `name="profanity" value="3" checked`) {
			t.Error("a level outside its dimension must not be trusted")
		}
	})
	t.Run("saving sets the cookie and answers with the card", func(t *testing.T) {
		w := postForm(h, "/settings/filters/fragment", url.Values{"profanity": {"1"}, "threats": {"2"}})
		cookies := w.Result().Cookies()
		if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != filterCookie || cookies[0].Value != "12012200" || !strings.Contains(w.Body.String(), "Saved.") || !strings.Contains(w.Body.String(), `name="threats" value="2" checked`) {
			t.Errorf("status %d, cookies %v; want the two choices saved over the defaults and the card returned", w.Code, cookies)
		}
	})
	t.Run("reset removes the cookie", func(t *testing.T) {
		w := postForm(h, "/settings/filters/fragment", url.Values{"reset": {"1"}}, &http.Cookie{Name: filterCookie, Value: "00000000"})
		cookies := w.Result().Cookies()
		if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].MaxAge >= 0 || !strings.Contains(w.Body.String(), `name="profanity" value="3" checked`) {
			t.Errorf("status %d; want the cookie expired and the defaults shown", w.Code)
		}
	})
	t.Run("a level the dimension does not have is refused", func(t *testing.T) {
		if w := postForm(h, "/settings/filters/fragment", url.Values{"adult": {"2"}}); w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
			t.Errorf("status %d; want 400 and no cookie", w.Code)
		}
	})
	t.Run("without scripts it returns to the settings page", func(t *testing.T) {
		if w := postForm(h, "/settings/filters", url.Values{"spam": {"0"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings#content-filters" {
			t.Errorf("status %d, location %q", w.Code, w.Header().Get("Location"))
		}
	})
}
