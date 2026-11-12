package server

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

var (
	mdAnyTagPattern = regexp.MustCompile(`<[^<>]*>`)
	mdTagPattern    = regexp.MustCompile(`^<(/?)([a-z0-9]+)((?:\s+[a-z0-9-]+="[^"]*")*)\s*(/?)>$`)
	mdAttrPattern   = regexp.MustCompile(`\s+([a-z0-9-]+)="([^"]*)"`)
	mdDigitsPattern = regexp.MustCompile(`^[0-9]+$`)
)

// Body is the post text as a card shows it. Markdown posts are rendered with
// external links inert until the reader confirms them on /leave; any other
// format is shown as literal text.
func (p post) Body() template.HTML {
	if p.format() != content.FormatMarkdown {
		return template.HTML(`<p class="post-text">` + template.HTMLEscapeString(p.Text) + "</p>")
	}
	if rendered, err := mdRender(p); err == nil {
		if markup, refs, ok := mdRewrite(string(rendered.HTML), p.ID, rendered.Links); ok {
			if popovers, ok := mdPopovers(p, refs); ok {
				return template.HTML(`<div class="post-text md-body reply-body">` + markup + popovers + "</div>")
			}
		}
	}
	return template.HTML(`<div class="post-text md-body reply-body"><p>` + template.HTMLEscapeString(p.Text) + `</p><p class="md-note">Shown as plain text</p></div>`)
}

// mdPopovers gives each reference the text uses a popover region after the
// text, holding the form that opens it, so the form never sits inside a link
// and the opened panel replaces the button that asked for it.
func mdPopovers(p post, refIDs []string) (string, bool) {
	var out strings.Builder
	for _, id := range refIDs {
		ref, declared := mdDeclaredRef(p, id)
		if !declared {
			return "", false
		}
		out.WriteString(`<div id="` + mdRegionID(p.ID, id) + `" class="md-ref-region" role="region" aria-label="Reference" aria-live="polite">` + mdRefOpen(p.ID, ref) + `</div>`)
	}
	return out.String(), true
}

// mdRefOpen is the button that opens one reference in its popover region;
// without scripts it opens the reference page.
func mdRefOpen(postID string, ref wire.PostReference) string {
	return `<form class="md-ref-form" action="/ref" method="get" get="/ref/fragment" target="#` + mdRegionID(postID, ref.ID) + `(innerHTML)" data-focus-after-swap=".md-ref-focus" timeout="5000">` +
		`<input type="hidden" name="post" value="` + template.HTMLEscapeString(postID) + `"><input type="hidden" name="ref" value="` + template.HTMLEscapeString(ref.ID) + `">` +
		`<button type="submit" class="btn-secondary md-ref-open">Show reference: <span class="md-target">` + template.HTMLEscapeString(mdShortTarget(ref.Target)) + `</span></button></form>`
}

// mdRender renders a post's Markdown with its declared references and media slots.
func mdRender(p post) (content.Rendered, error) {
	payload := p.Payload
	payload.ContentFormat = p.format()
	payload.Text = p.Text
	return content.Render(payload, content.RenderOptions{CheckURL: content.ValidateExternalURL})
}

// mdExternals keeps the external links of a rendered post in document order.
func mdExternals(links []content.Link) []content.Link {
	var external []content.Link
	for _, link := range links {
		if link.Kind == content.LinkExternal {
			external = append(external, link)
		}
	}
	return external
}

// mdDeclaredRef returns the reference a post declares under id, the first one when IDs repeat.
func mdDeclaredRef(p post, id string) (wire.PostReference, bool) {
	if p.Payload.Refs == nil {
		return wire.PostReference{}, false
	}
	for _, ref := range *p.Payload.Refs {
		if ref.ID == id {
			return ref, true
		}
	}
	return wire.PostReference{}, false
}

type mdSpan struct {
	class     string
	authority string
	target    string
}

// mdRewrite turns the trusted output of content.Render into the card markup.
// The n-th external span becomes a link to the confirmation page, the n-th
// reference span, when it matches the n-th declared reference Render
// reported, becomes a plain in-app link, and media placeholders become inline
// chips. It also returns the distinct reference IDs the text uses, in order.
// Anything unexpected makes it report false, so the caller falls back to
// literal text.
func mdRewrite(markup, postID string, links []content.Link) (string, []string, bool) {
	external := mdExternals(links)
	var declared []content.Link
	for _, link := range links {
		if link.Kind == content.LinkRef {
			declared = append(declared, link)
		}
	}
	var out strings.Builder
	var open []mdSpan
	var refIDs []string
	next, nextRef := 0, 0
	cursor := 0
	for _, location := range mdAnyTagPattern.FindAllStringIndex(markup, -1) {
		text := markup[cursor:location[0]]
		if strings.ContainsAny(text, "<>") {
			return "", nil, false
		}
		out.WriteString(text)
		cursor = location[1]
		match := mdTagPattern.FindStringSubmatch(markup[location[0]:location[1]])
		if match == nil {
			return "", nil, false
		}
		closing, name, selfClosing := match[1] == "/", match[2], match[4] == "/"
		attrs, ok := mdAttributes(match[3])
		if !ok || (closing && (len(attrs) > 0 || selfClosing)) {
			return "", nil, false
		}
		if name != "span" {
			if !mdAllowedTag(name, attrs) {
				return "", nil, false
			}
			out.WriteString(markup[location[0]:location[1]])
			continue
		}
		if closing {
			if len(open) == 0 {
				return "", nil, false
			}
			span := open[len(open)-1]
			open = open[:len(open)-1]
			switch span.class {
			case "fy-link":
				out.WriteString(`</a><span class="md-host">(` + template.HTMLEscapeString(span.authority) + `)</span>`)
			case "fy-ref":
				out.WriteString(`</a><span class="md-target">` + template.HTMLEscapeString(mdShortTarget(span.target)) + `</span>`)
			case "fy-media":
				out.WriteString("</span>")
			default:
				return "", nil, false
			}
			continue
		}
		class := attrs["class"]
		if selfClosing || len(attrs) == 0 {
			return "", nil, false
		}
		switch class {
		case "fy-link":
			if next >= len(external) || mdInLink(open) || html.UnescapeString(attrs["data-fy-url"]) != external[next].URL {
				return "", nil, false
			}
			link := external[next]
			out.WriteString(`<a class="md-link" href="/leave?post=` + url.QueryEscape(postID) + `&amp;link=` + strconv.Itoa(next) + `" rel="nofollow noopener noreferrer" referrerpolicy="no-referrer">`)
			next++
			open = append(open, mdSpan{class: class, authority: link.Authority})
		case "fy-ref":
			id, target := html.UnescapeString(attrs["data-fy-ref"]), html.UnescapeString(attrs["data-fy-target"])
			if nextRef >= len(declared) || id != declared[nextRef].RefID || target != declared[nextRef].Target || mdInLink(open) {
				return "", nil, false
			}
			nextRef++
			query := "post=" + url.QueryEscape(postID) + "&amp;ref=" + url.QueryEscape(id)
			out.WriteString(`<a class="md-ref" href="/ref?` + query + `">`)
			if !slices.Contains(refIDs, id) {
				refIDs = append(refIDs, id)
			}
			open = append(open, mdSpan{class: class, target: target})
		case "fy-media":
			kind, icon := "image", "🖼"
			if attrs["data-fy-kind"] == "video" {
				kind, icon = "video", "▶"
			}
			out.WriteString(`<span class="md-media"><span class="sr-only">` + kind + `: </span><span aria-hidden="true">` + icon + `</span> `)
			open = append(open, mdSpan{class: class})
		default:
			return "", nil, false
		}
	}
	if strings.ContainsAny(markup[cursor:], "<>") {
		return "", nil, false
	}
	out.WriteString(markup[cursor:])
	if len(open) != 0 || next != len(external) || nextRef != len(declared) {
		return "", nil, false
	}
	return out.String(), refIDs, true
}

// mdAttributes parses the attributes of one tag; a repeated attribute is malformed.
func mdAttributes(raw string) (map[string]string, bool) {
	attrs := make(map[string]string)
	for _, match := range mdAttrPattern.FindAllStringSubmatch(raw, -1) {
		if _, repeated := attrs[match[1]]; repeated {
			return nil, false
		}
		attrs[match[1]] = match[2]
	}
	return attrs, true
}

// mdAllowedTag reports whether Render may emit this non-span tag with these attributes.
func mdAllowedTag(name string, attrs map[string]string) bool {
	switch name {
	case "ol":
		start, ok := attrs["start"]
		return len(attrs) == 0 || (len(attrs) == 1 && ok && mdDigitsPattern.MatchString(start))
	case "div":
		return len(attrs) == 1 && attrs["class"] == "fy-literal"
	case "p", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "ul", "li", "pre", "code", "em", "strong", "br", "hr", "bdi":
		return len(attrs) == 0
	}
	return false
}

func mdInLink(open []mdSpan) bool {
	for _, span := range open {
		if span.class == "fy-link" || span.class == "fy-ref" {
			return true
		}
	}
	return false
}

// mdRegionID names the popover region of one reference in one post.
func mdRegionID(postID, refID string) string {
	sum := sha256.Sum256([]byte(postID + "\x00" + refID))
	return "md-ref-" + hex.EncodeToString(sum[:6])
}

// mdShortTarget shortens a canonical target for display; the full value stays in the reference page.
func mdShortTarget(target string) string {
	if len(target) <= 24 {
		return target
	}
	return target[:12] + "…" + target[len(target)-6:]
}
