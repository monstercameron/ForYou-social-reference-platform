package content

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func mdAcceptURL(value string) error {
	switch value {
	case "https://good.example/path", "https://auto.example/", "https://a.example/", "https://xn--bcher-kva.example/", "https://good.example/", "https://a.example/?a=1&b=2", "https://a.example/&amp;", "https://a.example/&#x1234567;":
		return nil
	default:
		return errors.New("rejected")
	}
}

func mdPayload(format, value string) wire.PostPayload {
	return wire.PostPayload{ContentFormat: format, Text: value}
}

func mdRender(t *testing.T, payload wire.PostPayload, options RenderOptions) Rendered {
	t.Helper()
	result, err := Render(payload, options)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRenderExplicitLinksAndInertSyntax(t *testing.T) {
	t.Run("only explicit links and URL autolinks activate", func(t *testing.T) {
		text := "https://bare.example/\n[x](https://good.example/path)\n<https://auto.example/>\n`[x](https://code.example/)`\n\\[x](https://escaped.example/)\n![x](https://image.example/)\n[x](https://title.example/ \"title\")\n```\n[x](https://fenced.example/)\n```"
		result := mdRender(t, mdPayload(FormatMarkdown, text), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 2 || result.Links[0].Authority != "good.example" || result.Links[1].Authority != "auto.example" {
			t.Fatalf("active links = %#v", result.Links)
		}
		if !reflect.DeepEqual(result.Media.InertImages, []string{"https://image.example/"}) {
			t.Fatalf("inert images = %#v", result.Media.InertImages)
		}
	})
	t.Run("blockquote multiline inline link remains title-free", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "> [x](https://a.example/\n> )"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].URL != "https://a.example/" {
			t.Fatalf("blockquote link = %#v", result.Links)
		}
	})
}

func TestRenderTitlesAndReferenceStyleStayInert(t *testing.T) {
	t.Run("empty inline link title", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, `[x](https://a.example/ "")`), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 0 || string(result.HTML) != "<p>x</p>\n" {
			t.Fatalf("rendered = %#v", result)
		}
	})
	t.Run("empty image title", func(t *testing.T) {
		slots := []wire.MediaReference{{ID: "cover", Kind: "image", URL: "https://media.example/a", SHA256: "abc"}}
		payload := mdPayload(FormatMarkdown, "![c](fy-media:cover '')")
		payload.MediaSlots = &slots
		result := mdRender(t, payload, RenderOptions{})
		if len(result.Media.Inline) != 0 || !reflect.DeepEqual(result.Media.Unreferenced, []string{"cover"}) {
			t.Fatalf("media plan = %#v", result.Media)
		}
	})
	t.Run("reference-style link", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[x][r]\n\n[r]: https://a.example/"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 0 || string(result.HTML) != "<p>x</p>\n" {
			t.Fatalf("rendered = %#v", result)
		}
	})
}

func TestRenderUnsafeDestinationsStayText(t *testing.T) {
	check := func(t *testing.T, destination string) {
		t.Helper()
		result := mdRender(t, mdPayload(FormatMarkdown, "[click]("+destination+")"), RenderOptions{CheckURL: mdAcceptURL})
		if string(result.HTML) != "<p>click</p>\n" || len(result.Links) != 0 {
			t.Fatalf("HTML %q, links %#v", result.HTML, result.Links)
		}
	}
	t.Run("http", func(t *testing.T) { check(t, "http://example.com/x") })
	t.Run("javascript", func(t *testing.T) { check(t, "javascript:alert") })
	t.Run("data", func(t *testing.T) { check(t, "data:text/html,x") })
	t.Run("file", func(t *testing.T) { check(t, "file:///etc/passwd") })
	t.Run("blob", func(t *testing.T) { check(t, "blob:https://example.com/x") })
	t.Run("mailto", func(t *testing.T) { check(t, "mailto:a@example.com") })
	t.Run("nil URL checker", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[x](https://good.example/)"), RenderOptions{})
		if len(result.Links) != 0 || strings.Contains(string(result.HTML), "fy-link") {
			t.Fatalf("nil checker activated link: %#v", result)
		}
	})
}

func TestRenderExternalLabelSpoofing(t *testing.T) {
	t.Run("URL-looking label exposes mismatch", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[https://example.com/](https://xn--bcher-kva.example/)"), RenderOptions{CheckURL: mdAcceptURL})
		want := `<p><span class="fy-link" data-fy-url="https://xn--bcher-kva.example/" data-fy-authority="xn--bcher-kva.example" data-fy-mismatch="true"><bdi>https://example.com/</bdi></span></p>` + "\n"
		if string(result.HTML) != want || len(result.Links) != 1 || result.Links[0].Authority != "xn--bcher-kva.example" || !result.Links[0].LabelMismatch {
			t.Fatalf("HTML %q; links %#v", result.HTML, result.Links)
		}
	})
	t.Run("autolink label matches destination", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "<https://good.example/>"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].LabelMismatch {
			t.Fatalf("links = %#v", result.Links)
		}
	})
	t.Run("soft line break remains in label", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[foo\nbar](https://a.example/)"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].Label != "foo\nbar" {
			t.Fatalf("links = %#v", result.Links)
		}
	})
}

func TestRenderParsedDestinations(t *testing.T) {
	t.Run("entity decoded once", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[x](https://a.example/?a=1&amp;b=2)"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].URL != "https://a.example/?a=1&b=2" {
			t.Fatalf("links = %#v", result.Links)
		}
	})
	t.Run("escaped ampersand is not decoded twice", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, `[x](https://a.example/\&amp;)`), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].URL != "https://a.example/&amp;" {
			t.Fatalf("links = %#v", result.Links)
		}
	})
	t.Run("numeric reference binds media ID", func(t *testing.T) {
		slots := []wire.MediaReference{{ID: "cover", Kind: "image", URL: "https://media.example/a", SHA256: "abc"}}
		payload := mdPayload(FormatMarkdown, "![c](fy-media:&#99;over)")
		payload.MediaSlots = &slots
		result := mdRender(t, payload, RenderOptions{})
		if !reflect.DeepEqual(result.Media.Inline, []string{"cover"}) {
			t.Fatalf("media plan = %#v", result.Media)
		}
	})
	t.Run("seven digit hexadecimal reference stays literal", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "[x](https://a.example/&#x1234567;)"), RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 1 || result.Links[0].URL != "https://a.example/&#x1234567;" {
			t.Fatalf("links = %#v", result.Links)
		}
	})
}

func TestRenderStructuredReferenceExample(t *testing.T) {
	t.Run("example renders its structured targets", func(t *testing.T) {
		// Targets and text are copied from examples/post-with-refs.json.
		refs := []wire.PostReference{{ID: "r1", Target: "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"}, {ID: "r2", Target: "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"}}
		payload := mdPayload(FormatMarkdown, "Hello [@reader](fy-ref:r1). See [this post](fy-ref:r2). An [unknown reference](fy-ref:missing) is inert.")
		payload.Refs = &refs
		result := mdRender(t, payload, RenderOptions{})
		want := `<p>Hello <span class="fy-ref" data-fy-ref="r1" data-fy-target="did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"><bdi>@reader</bdi></span>. See <span class="fy-ref" data-fy-ref="r2" data-fy-target="fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"><bdi>this post</bdi></span>. An unknown reference is inert.</p>` + "\n"
		if string(result.HTML) != want || len(result.Links) != 2 || result.Links[1].Target != refs[1].Target {
			t.Fatalf("HTML %q; links %#v", result.HTML, result.Links)
		}
	})
}

func TestRenderReferenceForms(t *testing.T) {
	t.Run("only title-free inline link to declared ref activates", func(t *testing.T) {
		refs := []wire.PostReference{{ID: "p1", Target: "fy1:" + strings.Repeat("c", 64)}}
		text := "fy-ref:p1\n`[p](fy-ref:p1)`\n\\[p](fy-ref:p1)\n![p](fy-ref:p1)\n[p](fy-ref:p1 \"title\")\n[p](fy-ref:p1)"
		payload := mdPayload(FormatMarkdown, text)
		payload.Refs = &refs
		result := mdRender(t, payload, RenderOptions{})
		if len(result.Links) != 1 || result.Links[0].Target != refs[0].Target {
			t.Fatalf("links = %#v", result.Links)
		}
	})
	t.Run("direct fy target stays inert", func(t *testing.T) { mdCheckInertReference(t, "[old](fy:fy1:"+strings.Repeat("c", 64)+")") })
	t.Run("query suffix stays inert", func(t *testing.T) { mdCheckInertReference(t, "[s](fy-ref:p1?x=1)") })
	t.Run("media destination as link stays inert", func(t *testing.T) { mdCheckInertReference(t, "[m](fy-media:one)") })
	t.Run("reference autolink stays inert", func(t *testing.T) { mdCheckInertReference(t, "<fy-ref:p1>") })
}

func mdCheckInertReference(t *testing.T, text string) {
	t.Helper()
	refs := []wire.PostReference{{ID: "p1", Target: "fy1:" + strings.Repeat("c", 64)}}
	payload := mdPayload(FormatMarkdown, text)
	payload.Refs = &refs
	result := mdRender(t, payload, RenderOptions{})
	if len(result.Links) != 0 {
		t.Fatalf("links = %#v", result.Links)
	}
}

func TestRenderMediaExample(t *testing.T) {
	t.Run("example binds photo and keeps clip unreferenced", func(t *testing.T) {
		// Slot metadata and text are copied from examples/post-with-media-slots.json.
		clipAlt := "Short clip"
		slots := []wire.MediaReference{
			{ID: "photo", URL: "https://media.example/sunset.jpg", Kind: "image", AltText: mdPtr("Sunset"), SHA256: "03c40629f930724f4724705524fc8c3d878697a8c4a6210ac4df06fcebfd9c44"},
			{ID: "clip", URL: "https://media.example/clip.mp4", Kind: "video", AltText: &clipAlt, SHA256: "4280f6bae17c31d302094e79f4c75a55c2c5bccf1067241a9454efed447f3a88"},
		}
		payload := mdPayload(FormatMarkdown, "An image: ![Sunset](fy-media:photo)")
		payload.MediaSlots = &slots
		result := mdRender(t, payload, RenderOptions{})
		want := `<p>An image: <span class="fy-media" data-fy-slot="photo" data-fy-kind="image" data-fy-url="https://media.example/sunset.jpg" data-fy-sha256="03c40629f930724f4724705524fc8c3d878697a8c4a6210ac4df06fcebfd9c44"><bdi>Sunset</bdi></span></p>` + "\n"
		if string(result.HTML) != want || !reflect.DeepEqual(result.Media.Inline, []string{"photo"}) || !reflect.DeepEqual(result.Media.Unreferenced, []string{"clip"}) {
			t.Fatalf("HTML %q; media %#v", result.HTML, result.Media)
		}
	})
}

func mdPtr(value string) *string { return &value }

func TestRenderMediaPlanBindsOnlyInlineFyMediaImages(t *testing.T) {
	slots := []wire.MediaReference{{ID: "cover", Kind: "image"}, {ID: "clip", Kind: "video"}, {ID: "extra", Kind: "image"}}
	t.Run("plan preserves order and ignores code and escaped image syntax", func(t *testing.T) {
		text := "![c](fy-media:cover) ![again](fy-media:cover)\n![m](fy-media:missing)\n![r](https://remote.example/a.png)\n`![code](fy-media:clip)`\n```\n![f](fy-media:clip)\n```\n[link](fy-media:clip) \\![esc](fy-media:extra) ![bad](fy-media:Cover)"
		payload := mdPayload(FormatMarkdown, text)
		payload.MediaSlots = &slots
		result := mdRender(t, payload, RenderOptions{})
		if !reflect.DeepEqual(result.Media.Inline, []string{"cover"}) || !reflect.DeepEqual(result.Media.UnboundIDs, []string{"missing", "Cover"}) || !reflect.DeepEqual(result.Media.Unreferenced, []string{"clip", "extra"}) || !reflect.DeepEqual(result.Media.InertImages, []string{"https://remote.example/a.png"}) {
			t.Fatalf("media plan = %#v", result.Media)
		}
	})
	t.Run("titled", func(t *testing.T) { mdCheckNoInlineMedia(t, `![c](fy-media:cover "title")`, slots) })
	t.Run("reference-style", func(t *testing.T) { mdCheckNoInlineMedia(t, "![c][r]\n\n[r]: fy-media:cover", slots) })
	t.Run("query suffix", func(t *testing.T) { mdCheckNoInlineMedia(t, "![c](fy-media:cover?x=1)", slots) })
	t.Run("fragment suffix", func(t *testing.T) { mdCheckNoInlineMedia(t, "![c](fy-media:cover#f)", slots) })
	t.Run("percent escape", func(t *testing.T) { mdCheckNoInlineMedia(t, "![c](fy-media:%63over)", slots) })
	t.Run("autolink", func(t *testing.T) { mdCheckNoInlineMedia(t, "<fy-media:cover>", slots) })
}

func mdCheckNoInlineMedia(t *testing.T, text string, slots []wire.MediaReference) {
	t.Helper()
	payload := mdPayload(FormatMarkdown, text)
	payload.MediaSlots = &slots
	result := mdRender(t, payload, RenderOptions{})
	if len(result.Media.Inline) != 0 {
		t.Fatalf("media plan = %#v", result.Media)
	}
}

func TestRenderRawHTMLIsLiteral(t *testing.T) {
	t.Run("script block", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "<script>alert(1)</script>"), RenderOptions{})
		if string(result.HTML) != `<div class="fy-literal">&lt;script&gt;alert(1)&lt;/script&gt;</div>`+"\n" {
			t.Fatalf("HTML = %q", result.HTML)
		}
	})
	t.Run("inline anchor", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, `<a href="https://evil.example/">x</a>`), RenderOptions{})
		if string(result.HTML) != `<p>&lt;a href=&quot;https://evil.example/&quot;&gt;x&lt;/a&gt;</p>`+"\n" {
			t.Fatalf("HTML = %q", result.HTML)
		}
	})
	check := func(t *testing.T, text string) {
		t.Helper()
		slots := []wire.MediaReference{{ID: "a", Kind: "image"}}
		payload := mdPayload(FormatMarkdown, text)
		payload.MediaSlots = &slots
		result := mdRender(t, payload, RenderOptions{CheckURL: mdAcceptURL})
		if len(result.Links) != 0 || len(result.Media.Inline) != 0 || strings.Contains(string(result.HTML), "<img") || strings.Contains(string(result.HTML), "<iframe") || strings.Contains(string(result.HTML), "<svg") || strings.Contains(string(result.HTML), "<script") || strings.Contains(string(result.HTML), "<a ") {
			t.Fatalf("unsafe output or activation: HTML %q, links %#v, media %#v", result.HTML, result.Links, result.Media)
		}
	}
	t.Run("HTML image", func(t *testing.T) { check(t, `<img src="fy-media:a">`) })
	t.Run("iframe", func(t *testing.T) { check(t, `<iframe src="https://evil.example/"></iframe>`) })
	t.Run("svg", func(t *testing.T) { check(t, `<svg onload="x()"></svg>`) })
}

func TestRenderPlainAndUnsupportedFormats(t *testing.T) {
	t.Run("plain text escapes syntax", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatPlain, `<b>[x](https://a.example/)</b>`), RenderOptions{CheckURL: mdAcceptURL})
		want := `<p class="fy-plain">&lt;b&gt;[x](https://a.example/)&lt;/b&gt;</p>` + "\n"
		if string(result.HTML) != want || len(result.Links) != 0 {
			t.Fatalf("plain output = %#v", result)
		}
	})
	t.Run("empty plain text", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatPlain, ""), RenderOptions{})
		if string(result.HTML) != "" {
			t.Fatalf("empty plain = %q", result.HTML)
		}
	})
	t.Run("unsupported format", func(t *testing.T) {
		if _, err := Render(mdPayload("text/html", "x"), RenderOptions{}); !errors.Is(err, ErrUnsupportedFormat) {
			t.Fatalf("unsupported error = %v", err)
		}
	})
}

func TestRenderCommonMarkFormatting(t *testing.T) {
	t.Run("blocks and inline formatting", func(t *testing.T) {
		text := "# Heading\n\n*em* **strong** `code`\n\n```go\nfmt.Println(1)\n```\n\n3. first\n4. second\n\nline  \nnext"
		result := mdRender(t, mdPayload(FormatMarkdown, text), RenderOptions{})
		want := "<h1>Heading</h1>\n<p><em>em</em> <strong>strong</strong> <code>code</code></p>\n<pre><code>fmt.Println(1)\n</code></pre>\n<ol start=\"3\">\n<li>first</li>\n<li>second</li>\n</ol>\n<p>line<br />\nnext</p>\n"
		if string(result.HTML) != want {
			t.Fatalf("HTML = %q; want %q", result.HTML, want)
		}
	})
	t.Run("loose item starts on a new line", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "- a\n\n- b"), RenderOptions{})
		want := "<ul>\n<li>\n<p>a</p>\n</li>\n<li>\n<p>b</p>\n</li>\n</ul>\n"
		if string(result.HTML) != want {
			t.Fatalf("HTML = %q; want %q", result.HTML, want)
		}
	})
	t.Run("tight paragraph before nested list", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "- a\n  - b"), RenderOptions{})
		want := "<ul>\n<li>a\n<ul>\n<li>b</li>\n</ul>\n</li>\n</ul>\n"
		if string(result.HTML) != want {
			t.Fatalf("HTML = %q; want %q", result.HTML, want)
		}
	})
	t.Run("code span line ending becomes space", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, "`a\nb`"), RenderOptions{})
		if string(result.HTML) != "<p><code>a b</code></p>\n" {
			t.Fatalf("HTML = %q", result.HTML)
		}
	})
	t.Run("indented code keeps tab padding", func(t *testing.T) {
		result := mdRender(t, mdPayload(FormatMarkdown, ">\t\tfoo"), RenderOptions{})
		if string(result.HTML) != "<blockquote>\n<pre><code>  foo\n</code></pre>\n</blockquote>\n" {
			t.Fatalf("HTML = %q", result.HTML)
		}
	})
}
