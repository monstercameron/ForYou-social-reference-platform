package server

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func a11yGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func a11yLuminance(hex string) float64 {
	channel := func(start int) float64 {
		v, _ := strconv.ParseUint(hex[start:start+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}

func a11yContrast(a, b string) float64 {
	la, lb := a11yLuminance(a), a11yLuminance(b)
	if lb > la {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func a11yToken(css, name string, occurrence int) string {
	matches := regexp.MustCompile(`--`+name+`:\s*(#[0-9a-fA-F]{6})`).FindAllStringSubmatch(css, -1)
	if occurrence >= len(matches) {
		return ""
	}
	return matches[occurrence][1]
}

func TestA11yShell(t *testing.T) {
	h := testClientHandler(t)
	t.Run("skip link comes before the sidebar", func(t *testing.T) {
		body := getWith(h, "/")
		skip := strings.Index(body, `class="a11y-skip button button-primary" href="#a11y-main">Skip to content</a>`)
		if skip < 0 || skip > strings.Index(body, `class="sidebar"`) {
			t.Error("the skip link should be the first control on the page, before the sidebar")
		}
	})
	t.Run("content start follows the mobile navigation", func(t *testing.T) {
		body := getWith(h, "/")
		start := strings.Index(body, `id="a11y-main" class="a11y-main-start" tabindex="-1"`)
		if start < strings.Index(body, `class="mobile-nav"`) || start > strings.Index(body, "<h1") {
			t.Error("the content start should follow the mobile navigation and precede the page heading")
		}
	})
	t.Run("one live region and the script", func(t *testing.T) {
		body := getWith(h, "/settings")
		if strings.Count(body, `id="a11y-live"`) != 1 || !strings.Contains(body, `id="a11y-live" class="sr-only" role="status" aria-live="polite" aria-atomic="true"`) {
			t.Error("each page should have one polite status region for announcements")
		}
		if !strings.Contains(body, `<script type="module" src="/static/a11y.js"></script>`) {
			t.Error("the page should load the announcement script")
		}
	})
	t.Run("script is served without network or storage use", func(t *testing.T) {
		w := a11yGet(h, "/static/a11y.js")
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "MutationObserver") || !strings.Contains(body, "a11y-live") {
			t.Fatal("the announcement script should be served and name the live region")
		}
		if strings.Contains(body, "fetch(") || strings.Contains(body, "localStorage") || strings.Contains(body, "sessionStorage") || strings.Contains(body, "document.cookie") {
			t.Error("the announcement script must make no network request and use no storage or cookie")
		}
	})
}

func TestA11yOneHeading(t *testing.T) {
	h := testClientHandler(t)
	t.Run("Home", func(t *testing.T) {
		if strings.Count(getWith(h, "/"), "<h1") != 1 {
			t.Error("Home should have exactly one heading level 1")
		}
	})
	t.Run("Explore", func(t *testing.T) {
		if strings.Count(getWith(h, "/explore"), "<h1") != 1 {
			t.Error("Explore should have exactly one heading level 1")
		}
	})
	t.Run("Settings", func(t *testing.T) {
		if strings.Count(getWith(h, "/settings"), "<h1") != 1 {
			t.Error("Settings should have exactly one heading level 1")
		}
	})
	t.Run("New post", func(t *testing.T) {
		if strings.Count(getWith(h, "/compose"), "<h1") != 1 {
			t.Error("New post should have exactly one heading level 1")
		}
	})
	t.Run("not found page", func(t *testing.T) {
		if strings.Count(getWith(h, "/no-such-page"), "<h1") != 1 {
			t.Error("the not found page should have exactly one heading level 1")
		}
	})
}

func TestA11yStyles(t *testing.T) {
	t.Run("imported last", func(t *testing.T) {
		css, err := os.ReadFile("styles/tailwind.css")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Index(string(css), `@import "./a11y.css";`) < strings.Index(string(css), `@import "./responsive.css";`) {
			t.Error("a11y.css should be imported after responsive.css so its overrides win ties")
		}
	})
	t.Run("control border passes 3 to 1 in both themes", func(t *testing.T) {
		css, err := os.ReadFile("styles/a11y.css")
		if err != nil {
			t.Fatal(err)
		}
		if a11yContrast("#848b9e", "#f0f1f6") < 3 || a11yContrast("#6b7489", "#262b39") < 3 {
			t.Error("the control border should contrast at least 3 to 1 with the control in both themes")
		}
		if !strings.Contains(string(css), "#848b9e") || !strings.Contains(string(css), "#6b7489") {
			t.Error("a11y.css should set the light and dark control borders")
		}
	})
	t.Run("base text tokens pass 4.5 to 1", func(t *testing.T) {
		css, err := os.ReadFile("styles/base.css")
		if err != nil {
			t.Fatal(err)
		}
		source := string(css)
		muted, control := a11yToken(source, "muted", 0), a11yToken(source, "control", 0)
		accent, accentSoft := a11yToken(source, "accent", 1), a11yToken(source, "accent-soft", 1)
		if muted == "" || control == "" || accent == "" || accentSoft == "" {
			t.Fatal("base.css should define light --muted and --control and dark --accent and --accent-soft as six-digit hex colours")
		}
		if a11yContrast(muted, control) < 4.5 {
			t.Error("light muted text on the control surface should pass 4.5 to 1")
		}
		if a11yContrast(accent, accentSoft) < 4.5 {
			t.Error("dark accent text on its soft surface should pass 4.5 to 1")
		}
	})
}
