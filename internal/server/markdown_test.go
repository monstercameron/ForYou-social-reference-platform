package server

import (
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func TestMarkdownBody(t *testing.T) {
	body := func(text string) string {
		return string(post{ID: "fy1:p1", Format: "text/markdown", Text: text}.Body())
	}
	t.Run("external link becomes a confirmation link with its host", func(t *testing.T) {
		got := body("[docs](https://example.com/a)")
		if !strings.Contains(got, `<a class="md-link" href="/leave?post=fy1%3Ap1&amp;link=0"`) || !strings.Contains(got, `<span class="md-host">(example.com)</span>`) {
			t.Errorf("external link body = %q", got)
		}
	})
	t.Run("angle-bracket autolink becomes a confirmation link", func(t *testing.T) {
		if got := body("<https://example.com>"); !strings.Contains(got, `href="/leave?post=fy1%3Ap1&amp;link=0"`) {
			t.Errorf("autolink body = %q", got)
		}
	})
	t.Run("bare URL stays text", func(t *testing.T) {
		if got := body("https://example.com"); strings.Contains(got, "md-link") {
			t.Errorf("bare URL became a link: %q", got)
		}
	})
	t.Run("http destination stays inert", func(t *testing.T) {
		if got := body("[x](http://example.com)"); strings.Contains(got, "md-link") {
			t.Errorf("http destination became a link: %q", got)
		}
	})
	t.Run("javascript destination stays inert", func(t *testing.T) {
		if got := body("[x](javascript:alert(1))"); strings.Contains(got, "md-link") {
			t.Errorf("javascript destination became a link: %q", got)
		}
	})
	t.Run("titled link stays inert", func(t *testing.T) {
		if got := body(`[x](https://example.com "title")`); strings.Contains(got, "md-link") {
			t.Errorf("titled link became a link: %q", got)
		}
	})
	t.Run("raw HTML is escaped text", func(t *testing.T) {
		got := body("<script>alert(1)</script>")
		if !strings.Contains(got, "&lt;script&gt;") || strings.Contains(got, "<script") {
			t.Errorf("raw HTML body = %q", got)
		}
	})
	t.Run("plain text is never parsed as Markdown", func(t *testing.T) {
		got := string(post{ID: "fy1:p1", Format: "text/plain", Text: "[x](https://example.com)"}.Body())
		if got != `<p class="post-text">[x](https://example.com)</p>` {
			t.Errorf("plain body = %q", got)
		}
	})
	t.Run("declared reference becomes an in-app link", func(t *testing.T) {
		refs := []wire.PostReference{{ID: "r1", Target: "fy1:target"}}
		p := post{ID: "fy1:p1", Format: "text/markdown", Text: "See [this](fy-ref:r1) and [that](fy-ref:missing).", Payload: wire.PostPayload{Refs: &refs}}
		got := string(p.Body())
		if !strings.Contains(got, `<a class="md-ref" href="/ref?post=fy1%3Ap1&amp;ref=r1"><bdi>this</bdi></a><span class="md-target">fy1:target</span>`) {
			t.Errorf("reference body = %q", got)
		}
		if !strings.Contains(got, `<form class="md-ref-form" action="/ref" method="get" get="/ref/fragment" target="#md-ref-`) || !strings.Contains(got, `<input type="hidden" name="ref" value="r1">`) {
			t.Errorf("reference popover is missing: %q", got)
		}
		if !strings.Contains(got, `class="md-ref-region" role="region" aria-label="Reference" aria-live="polite"><form class="md-ref-form"`) {
			t.Errorf("open button is not inside its popover region: %q", got)
		}
		if strings.Count(got, `class="md-ref"`) != 1 {
			t.Errorf("reference link count is wrong: %q", got)
		}
	})
	t.Run("undeclared reference stays text outside any link", func(t *testing.T) {
		refs := []wire.PostReference{{ID: "r1", Target: "fy1:target"}}
		p := post{ID: "fy1:p1", Format: "text/markdown", Text: "See [this](fy-ref:r1) and [that](fy-ref:missing).", Payload: wire.PostPayload{Refs: &refs}}
		got := string(p.Body())
		if !strings.Contains(got, `</span> and that.`) {
			t.Errorf("undeclared reference label is not outside a link: %q", got)
		}
		if strings.Contains(got, "fy-ref:missing") || strings.Count(got, `class="md-ref"`) != 1 {
			t.Errorf("undeclared reference should stay text: %q", got)
		}
	})
	t.Run("reference used twice has one link target and one popover", func(t *testing.T) {
		refs := []wire.PostReference{{ID: "r1", Target: "fy1:target"}}
		p := post{ID: "fy1:p1", Format: "text/markdown", Text: "[a](fy-ref:r1) [b](fy-ref:r1)", Payload: wire.PostPayload{Refs: &refs}}
		got := string(p.Body())
		if strings.Count(got, `class="md-ref-form"`) != 1 || strings.Count(got, `class="md-ref-region"`) != 1 || strings.Count(got, `class="md-ref"`) != 2 {
			t.Errorf("repeated reference body = %q", got)
		}
	})
	t.Run("reference-style link with a definition stays inert", func(t *testing.T) {
		got := body("[x][r]\n\n[r]: https://example.com")
		if strings.Contains(got, "<a") || !strings.Contains(got, "x") {
			t.Errorf("reference-style link body = %q", got)
		}
	})
	t.Run("spec example: declared references open in app, the unknown one stays text", func(t *testing.T) {
		refs := []wire.PostReference{{ID: "r1", Target: "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"}, {ID: "r2", Target: "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"}}
		p := post{ID: "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18", Format: "text/markdown", Text: "Hello [@reader](fy-ref:r1). See [this post](fy-ref:r2). An [unknown reference](fy-ref:missing) is inert.", Payload: wire.PostPayload{Refs: &refs}}
		got := string(p.Body())
		if !strings.Contains(got, `<a class="md-ref" href="/ref?post=fy1%3A2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18&amp;ref=r1">`) {
			t.Errorf("r1 is not an in-app link: %q", got)
		}
		if !strings.Contains(got, `<a class="md-ref" href="/ref?post=fy1%3A2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18&amp;ref=r2">`) {
			t.Errorf("r2 is not an in-app link: %q", got)
		}
		if !strings.Contains(got, " An unknown reference is inert.") || strings.Contains(got, "fy-ref:missing") {
			t.Errorf("missing reference is not inert text: %q", got)
		}
	})
	t.Run("media placeholder is a chip with a screen reader label", func(t *testing.T) {
		alt := "harbor"
		slots := []wire.MediaReference{{ID: "landscape-1", URL: "https://media.example/a.png", Kind: "image", SHA256: "00", AltText: &alt}}
		p := post{ID: "fy1:p1", Format: "text/markdown", Text: "![harbor](fy-media:landscape-1)", Payload: wire.PostPayload{MediaSlots: &slots}}
		if got := string(p.Body()); !strings.Contains(got, `<span class="sr-only">image: </span>`) || !strings.Contains(got, `aria-hidden="true">🖼</span>`) {
			t.Errorf("media body = %q", got)
		}
	})
	t.Run("unexpected markup falls back to plain text", func(t *testing.T) {
		if _, _, ok := mdRewrite(`<p><script>alert(1)</script></p>`, "fy1:p1", nil); ok {
			t.Error("script tag was accepted by the rewriter")
		}
		if _, _, ok := mdRewrite(`<p><a href="https://evil.example">x</a></p>`, "fy1:p1", nil); ok {
			t.Error("anchor from outside Render was accepted by the rewriter")
		}
		if _, _, ok := mdRewrite(`<p><span class="fy-ref" data-fy-ref="r9" data-fy-target="fy1:other"><bdi>x</bdi></span></p>`, "fy1:p1", nil); ok {
			t.Error("reference span that Render did not report was accepted by the rewriter")
		}
	})
}
