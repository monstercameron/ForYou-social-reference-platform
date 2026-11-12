package server

import (
	"regexp"
	"strings"
	"testing"
)

func TestDOMKey(t *testing.T) {
	t.Run("a message ID loses its colon", func(t *testing.T) {
		if got := domKey("fy1:ab12"); got != "fy1-ab12" {
			t.Errorf("domKey = %q; want fy1-ab12", got)
		}
	})
	t.Run("a DID loses both colons", func(t *testing.T) {
		if got := domKey("did:key:z6MkExample"); got != "did-key-z6MkExample" {
			t.Errorf("domKey = %q; want did-key-z6MkExample", got)
		}
	})
	t.Run("a demo ID is unchanged", func(t *testing.T) {
		if got := domKey("ad-coffee_2"); got != "ad-coffee_2" {
			t.Errorf("domKey = %q; want ad-coffee_2", got)
		}
	})
}

func TestCardsAddressElementsWithoutColons(t *testing.T) {
	client := newTestClient(t, false)
	id := "fy1:" + strings.Repeat("ab", 32)
	p := post{ID: id, Text: "A post whose ID has a colon.", Receipt: "ab…ab", AuthorID: "did:key:z6MkgFMdkUdT5dYyzMgBHmCBnPvwEFS7pZkjGnLCVG4coNEM", Fingerprint: "aaaa bbbb cccc dddd", ReplyCount: 2}
	var out strings.Builder
	if err := client.app.templates.ExecuteTemplate(&out, "card-post-list", []post{p}); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	addressed := regexp.MustCompile(`\b(id|for|target|onerror|aria-controls|aria-labelledby|aria-describedby)=("[^"]*:[^"]*"|'[^']*:[^']*')`)
	t.Run("no element ID or update target holds a colon", func(t *testing.T) {
		if found := addressed.FindAllString(body, -1); len(found) != 0 {
			t.Errorf("attributes with a colon: %q", found)
		}
	})
	t.Run("the thread region is addressed by the key", func(t *testing.T) {
		key := "fy1-" + strings.Repeat("ab", 32)
		if !strings.Contains(body, `id="thread-`+key+`"`) || !strings.Contains(body, `target="#thread-`+key+`(innerHTML)"`) {
			t.Error("the card does not carry a thread region and a target that share one colon-free key")
		}
	})
	t.Run("links and form fields keep the real ID", func(t *testing.T) {
		if !strings.Contains(body, `href="/posts/`+id+`"`) || !strings.Contains(body, `name="id" value="`+id+`"`) {
			t.Error("the card's link or hidden field lost the real message ID")
		}
	})
}
