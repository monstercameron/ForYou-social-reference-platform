package content

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/wire"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

const (
	// FormatPlain identifies literal post text.
	FormatPlain = "text/plain"
	// FormatMarkdown identifies CommonMark post text.
	FormatMarkdown = "text/markdown"
)

// ErrUnsupportedFormat identifies an unsupported post content format.
var ErrUnsupportedFormat = errors.New("content: unsupported content_format")

// RenderOptions configures Render.
type RenderOptions struct {
	// CheckURL validates an external destination; nil makes every external link inert.
	CheckURL func(string) error
}

// LinkKind identifies the kind of active link.
type LinkKind int

const (
	// LinkExternal identifies a validated external URL.
	LinkExternal LinkKind = iota + 1
	// LinkRef identifies a declared structured reference.
	LinkRef
)

// Link is an active link; Label is untrusted author text and never a verified name.
type Link struct {
	Kind          LinkKind
	Label         string
	URL           string // LinkExternal
	Authority     string // LinkExternal
	LabelMismatch bool   // LinkExternal
	RefID         string // LinkRef
	Target        string // LinkRef: canonical fy1: message ID or did:key
}

// MediaPlan lists inline and unreferenced slots and inert media syntax.
type MediaPlan struct {
	Inline       []string
	Unreferenced []string
	UnboundIDs   []string
	InertImages  []string
}

// Rendered is a safe HTML fragment and its activation plans.
type Rendered struct {
	HTML  template.HTML
	Links []Link
	Media MediaPlan
}

// Render parses a post and returns an inert HTML fragment and activation plans.
func Render(payload wire.PostPayload, options RenderOptions) (Rendered, error) {
	result := Rendered{Links: []Link{}}
	if payload.ContentFormat == FormatPlain {
		result.Media.Unreferenced = mdDeclaredSlots(payload)
		if payload.Text == "" {
			return result, nil
		}
		result.HTML = template.HTML(`<p class="fy-plain">` + mdEscape(payload.Text) + "</p>\n")
		return result, nil
	}
	if payload.ContentFormat != FormatMarkdown {
		return Rendered{}, ErrUnsupportedFormat
	}
	parser := mdNewParser()
	source := []byte(payload.Text)
	document := parser.Parser().Parse(text.NewReader(source))
	refs := make(map[string]string)
	if payload.Refs != nil {
		for _, ref := range *payload.Refs {
			if _, exists := refs[ref.ID]; !exists {
				refs[ref.ID] = ref.Target
			}
		}
	}
	slots := make(map[string]wire.MediaReference)
	if payload.MediaSlots != nil {
		for _, slot := range *payload.MediaSlots {
			if _, exists := slots[slot.ID]; !exists {
				slots[slot.ID] = slot
			}
		}
	}
	renderer := mdRenderer{source: source, titles: parser.titles, refs: refs, slots: slots, options: options, links: []Link{}, bound: make(map[string]bool)}
	if err := renderer.blocks(document); err != nil {
		return Rendered{}, err
	}
	result.HTML = template.HTML(renderer.output.String())
	result.Links = renderer.links
	result.Media = renderer.media
	for _, slot := range mdDeclaredSlots(payload) {
		if !renderer.bound[slot] {
			result.Media.Unreferenced = append(result.Media.Unreferenced, slot)
		}
	}
	return result, nil
}

type mdRenderer struct {
	source  []byte
	titles  map[ast.Node]bool
	refs    map[string]string
	slots   map[string]wire.MediaReference
	options RenderOptions
	output  strings.Builder
	links   []Link
	media   MediaPlan
	bound   map[string]bool
}

func (r *mdRenderer) block(node ast.Node) error {
	switch value := node.(type) {
	case *ast.Paragraph:
		r.output.WriteString("<p>")
		if err := r.inlines(node); err != nil {
			return err
		}
		r.output.WriteString("</p>\n")
	case *ast.TextBlock:
		if err := r.inlines(node); err != nil {
			return err
		}
	case *ast.Heading:
		if value.Level < 1 || value.Level > 6 {
			return fmt.Errorf("content: unsupported heading level %d", value.Level)
		}
		tag := "h" + strconv.Itoa(value.Level)
		r.output.WriteByte('<')
		r.output.WriteString(tag)
		r.output.WriteByte('>')
		if err := r.inlines(node); err != nil {
			return err
		}
		r.output.WriteString("</")
		r.output.WriteString(tag)
		r.output.WriteString(">\n")
	case *ast.Blockquote:
		r.output.WriteString("<blockquote>\n")
		if err := r.blocks(node); err != nil {
			return err
		}
		r.output.WriteString("</blockquote>\n")
	case *ast.List:
		tag := "ul"
		if value.IsOrdered() {
			tag = "ol"
		}
		r.output.WriteByte('<')
		r.output.WriteString(tag)
		if value.IsOrdered() && value.Start != 1 {
			r.output.WriteString(` start="`)
			r.output.WriteString(strconv.Itoa(value.Start))
			r.output.WriteByte('"')
		}
		r.output.WriteString(">\n")
		if err := r.blocks(node); err != nil {
			return err
		}
		r.output.WriteString("</")
		r.output.WriteString(tag)
		r.output.WriteString(">\n")
	case *ast.ListItem:
		list, ok := value.Parent().(*ast.List)
		if !ok {
			return fmt.Errorf("content: list item outside a list")
		}
		r.output.WriteString("<li>")
		if first := value.FirstChild(); first != nil && !(list.IsTight && mdIsParagraph(first)) {
			r.output.WriteByte('\n')
		}
		for child := value.FirstChild(); child != nil; child = child.NextSibling() {
			if list.IsTight && mdIsParagraph(child) {
				if err := r.inlines(child); err != nil {
					return err
				}
				if child.NextSibling() != nil {
					r.output.WriteByte('\n')
				}
				continue
			}
			if err := r.block(child); err != nil {
				return err
			}
		}
		r.output.WriteString("</li>\n")
	case *ast.CodeBlock:
		r.output.WriteString("<pre><code>")
		r.writeBlockLines(value)
		r.output.WriteString("</code></pre>\n")
	case *ast.FencedCodeBlock:
		r.output.WriteString("<pre><code>")
		r.writeBlockLines(value)
		r.output.WriteString("</code></pre>\n")
	case *ast.HTMLBlock:
		r.output.WriteString(`<div class="fy-literal">`)
		r.output.WriteString(mdEscape(mdHTMLBlockText(value, r.source)))
		r.output.WriteString("</div>\n")
	case *ast.ThematicBreak:
		r.output.WriteString("<hr />\n")
	case *ast.LinkReferenceDefinition:
		return nil
	default:
		return fmt.Errorf("content: unsupported block node %T", node)
	}
	return nil
}

func (r *mdRenderer) blocks(parent ast.Node) error {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if err := r.block(child); err != nil {
			return err
		}
	}
	return nil
}

// mdIsParagraph reports whether a list item child is a paragraph, which a tight list renders without <p>.
func mdIsParagraph(node ast.Node) bool {
	switch node.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return true
	}
	return false
}

func (r *mdRenderer) writeBlockLines(node ast.Node) {
	lines := node.Lines()
	for i := range lines.Len() {
		segment := lines.At(i)
		r.output.WriteString(mdEscape(string(segment.Value(r.source))))
	}
}

func (r *mdRenderer) inlines(parent ast.Node) error {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if err := r.inline(child); err != nil {
			return err
		}
	}
	return nil
}

func (r *mdRenderer) inline(node ast.Node) error {
	switch value := node.(type) {
	case *ast.Text:
		r.output.WriteString(mdWriteText(value.Text(r.source)))
		if value.HardLineBreak() {
			r.output.WriteString("<br />\n")
		} else if value.SoftLineBreak() {
			r.output.WriteByte('\n')
		}
	case *ast.String:
		r.output.WriteString(mdWriteText(value.Value))
	case *ast.CodeSpan:
		r.output.WriteString("<code>")
		code := strings.ReplaceAll(string(value.Text(r.source)), "\r\n", " ")
		code = strings.ReplaceAll(code, "\r", " ")
		code = strings.ReplaceAll(code, "\n", " ")
		r.output.WriteString(mdEscape(code))
		r.output.WriteString("</code>")
	case *ast.Emphasis:
		tag := "em"
		if value.Level == 2 {
			tag = "strong"
		}
		r.output.WriteByte('<')
		r.output.WriteString(tag)
		r.output.WriteByte('>')
		if err := r.inlines(node); err != nil {
			return err
		}
		r.output.WriteString("</")
		r.output.WriteString(tag)
		r.output.WriteByte('>')
	case *ast.Link:
		return r.link(value)
	case *ast.Image:
		return r.image(value)
	case *ast.AutoLink:
		label := string(value.Label(r.source))
		if value.AutoLinkType == ast.AutoLinkURL && r.options.CheckURL != nil {
			destination := string(value.URL(r.source))
			if r.options.CheckURL(destination) == nil {
				r.beginExternal(label, destination)
				r.output.WriteString(mdEscape(label) + "</bdi></span>")
				return nil
			}
		}
		r.output.WriteString(mdEscape(label))
	case *ast.RawHTML:
		for i := range value.Segments.Len() {
			segment := value.Segments.At(i)
			r.output.WriteString(mdEscape(string(segment.Value(r.source))))
		}
	case *ast.LinkReferenceDefinition:
		return nil
	default:
		return fmt.Errorf("content: unsupported inline node %T", node)
	}
	return nil
}

func (r *mdRenderer) link(node *ast.Link) error {
	label := mdLabel(node, r.source)
	if node.Reference == nil && !r.titles[node] {
		destination := mdParsedDestination(node.Destination)
		if strings.HasPrefix(destination, "fy-ref:") {
			id := strings.TrimPrefix(destination, "fy-ref:")
			if mdIDPattern.MatchString(id) {
				if target, ok := r.refs[id]; ok {
					r.links = append(r.links, Link{Kind: LinkRef, Label: label, RefID: id, Target: target})
					r.output.WriteString(`<span class="fy-ref" data-fy-ref="` + mdEscape(id) + `" data-fy-target="` + mdEscape(target) + `"><bdi>`)
					if err := r.inlines(node); err != nil {
						return err
					}
					r.output.WriteString("</bdi></span>")
					return nil
				}
			}
		}
		if !strings.HasPrefix(destination, "fy-media:") && r.options.CheckURL != nil && r.options.CheckURL(destination) == nil {
			return r.externalNodeWithChildren(node, label, destination)
		}
	}
	return r.inlines(node)
}

func (r *mdRenderer) beginExternal(label, destination string) {
	authority := mdAuthority(destination)
	link := Link{Kind: LinkExternal, Label: label, URL: destination, Authority: authority, LabelMismatch: mdURLLabelPattern.MatchString(label) && label != destination}
	r.links = append(r.links, link)
	r.output.WriteString(`<span class="fy-link" data-fy-url="` + mdEscape(destination) + `" data-fy-authority="` + mdEscape(authority) + `"`)
	if link.LabelMismatch {
		r.output.WriteString(` data-fy-mismatch="true"`)
	}
	r.output.WriteString("><bdi>")
}

func (r *mdRenderer) image(node *ast.Image) error {
	destination := mdParsedDestination(node.Destination)
	label := mdLabel(node, r.source)
	if strings.HasPrefix(destination, "fy-media:") {
		id := strings.TrimPrefix(destination, "fy-media:")
		if node.Reference == nil && !r.titles[node] && mdIDPattern.MatchString(id) {
			if slot, ok := r.slots[id]; ok {
				if !r.bound[id] {
					r.bound[id] = true
					r.media.Inline = append(r.media.Inline, id)
				}
				alt := label
				if slot.AltText != nil && *slot.AltText != "" {
					alt = *slot.AltText
				}
				r.output.WriteString(`<span class="fy-media" data-fy-slot="` + mdEscape(id) + `" data-fy-kind="` + mdEscape(slot.Kind) + `" data-fy-url="` + mdEscape(slot.URL) + `" data-fy-sha256="` + mdEscape(slot.SHA256) + `"`)
				if slot.SizeBytes != 0 {
					r.output.WriteString(` data-fy-size="` + strconv.FormatInt(slot.SizeBytes, 10) + `"`)
				}
				r.output.WriteString("><bdi>" + mdEscape(alt) + "</bdi></span>")
				return nil
			}
		}
		r.media.UnboundIDs = append(r.media.UnboundIDs, id)
	} else {
		r.media.InertImages = append(r.media.InertImages, destination)
	}
	r.output.WriteString(mdEscape(label))
	return nil
}

func (r *mdRenderer) externalNodeWithChildren(node *ast.Link, label, destination string) error {
	r.beginExternal(label, destination)
	if err := r.inlines(node); err != nil {
		return err
	}
	r.output.WriteString("</bdi></span>")
	return nil
}

func mdAuthority(destination string) string {
	if !strings.HasPrefix(destination, "https://") {
		return ""
	}
	authority := destination[len("https://"):]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return authority
}

func mdHTMLBlockText(node *ast.HTMLBlock, source []byte) string {
	var lines []string
	for i := range node.Lines().Len() {
		segment := node.Lines().At(i)
		lines = append(lines, strings.TrimSuffix(strings.TrimSuffix(string(segment.Value(source)), "\n"), "\r"))
	}
	if node.HasClosure() {
		segment := node.ClosureLine
		lines = append(lines, strings.TrimSuffix(strings.TrimSuffix(string(segment.Value(source)), "\n"), "\r"))
	}
	return strings.Join(lines, "\n")
}

func mdEscape(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch r {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '"':
			out.WriteString("&quot;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// mdWriteText applies CommonMark backslash-escape and character-reference processing to text and escapes the result.
func mdWriteText(value []byte) string {
	var out bytes.Buffer
	writer := bufio.NewWriter(&out)
	html.NewWriter().Write(writer, value)
	_ = writer.Flush()
	return out.String()
}

func mdDeclaredSlots(payload wire.PostPayload) []string {
	var ids []string
	if payload.MediaSlots != nil {
		for _, slot := range *payload.MediaSlots {
			ids = append(ids, slot.ID)
		}
	}
	return ids
}
