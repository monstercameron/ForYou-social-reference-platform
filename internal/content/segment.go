package content

import (
	stdhtml "html"
	"strings"

	"foryou.local/foryou/internal/wire"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Segments is a post's text split for analysis: prose, code and active links.
type Segments struct {
	Prose       []string // text of non-code inline nodes, one string per leaf block; link labels included, destinations excluded
	CodeBlocks  []string // indented and fenced code block contents
	CodeSpans   []string // code span contents
	ActiveLinks []string // external destinations Render makes active under ValidateExternalURL
	Structure   Structure
}

// Structure counts the block structure of a Markdown post.
type Structure struct {
	Lists, Headings, Quotes, Paragraphs int
}

// Segment splits a validated payload into prose, code and active links using the
// same CommonMark profile as Render.
func Segment(p wire.PostPayload) (Segments, error) {
	if p.ContentFormat != FormatPlain && p.ContentFormat != FormatMarkdown {
		return Segments{}, ErrUnsupportedFormat
	}
	if p.Text == "" {
		return Segments{}, nil
	}
	if p.ContentFormat == FormatPlain {
		return Segments{
			Prose:     []string{p.Text},
			Structure: Structure{Paragraphs: segPlainParagraphs(p.Text)},
		}, nil
	}
	parser := mdNewParser()
	source := []byte(p.Text)
	document := parser.Parser().Parse(text.NewReader(source))
	result := Segments{}
	segWalk(document, source, &result)
	rendered, err := Render(p, RenderOptions{CheckURL: ValidateExternalURL})
	if err != nil {
		return Segments{}, err
	}
	for _, link := range rendered.Links {
		if link.Kind == LinkExternal {
			result.ActiveLinks = append(result.ActiveLinks, link.URL)
		}
	}
	return result, nil
}

func segPlainParagraphs(value string) int {
	paragraphs := 0
	inParagraph := false
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) == "" {
			inParagraph = false
			continue
		}
		if !inParagraph {
			paragraphs++
			inParagraph = true
		}
	}
	return paragraphs
}

func segWalk(node ast.Node, source []byte, result *Segments) {
	switch value := node.(type) {
	case *ast.List:
		result.Structure.Lists++
	case *ast.Heading:
		result.Structure.Headings++
		segAppendProse(node, source, result)
		return
	case *ast.Blockquote:
		result.Structure.Quotes++
	case *ast.Paragraph:
		result.Structure.Paragraphs++
		segAppendProse(node, source, result)
		return
	case *ast.TextBlock:
		segAppendProse(node, source, result)
		return
	case *ast.HTMLBlock:
		if prose := mdHTMLBlockText(value, source); prose != "" {
			result.Prose = append(result.Prose, prose)
		}
		return
	case *ast.CodeBlock:
		result.CodeBlocks = append(result.CodeBlocks, segBlockLines(value, source))
		return
	case *ast.FencedCodeBlock:
		result.CodeBlocks = append(result.CodeBlocks, segBlockLines(value, source))
		return
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		segWalk(child, source, result)
	}
}

func segAppendProse(node ast.Node, source []byte, result *Segments) {
	if prose := segInlineText(node, source, result); prose != "" {
		result.Prose = append(result.Prose, prose)
	}
}

func segBlockLines(node ast.Node, source []byte) string {
	var out strings.Builder
	lines := node.Lines()
	for i := range lines.Len() {
		segment := lines.At(i)
		out.Write(segment.Value(source))
	}
	return out.String()
}

func segInlineText(parent ast.Node, source []byte, result *Segments) string {
	var out strings.Builder
	var visit func(ast.Node)
	visit = func(node ast.Node) {
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			switch value := child.(type) {
			case *ast.Text:
				out.WriteString(stdhtml.UnescapeString(mdWriteText(value.Text(source))))
				if value.SoftLineBreak() || value.HardLineBreak() {
					out.WriteByte('\n')
				}
			case *ast.String:
				out.Write(value.Value)
			case *ast.Emphasis, *ast.Link, *ast.Image:
				visit(child)
			case *ast.RawHTML:
				for i := range value.Segments.Len() {
					segment := value.Segments.At(i)
					out.Write(segment.Value(source))
				}
			case *ast.AutoLink:
				if value.AutoLinkType != ast.AutoLinkURL || ValidateExternalURL(string(value.URL(source))) != nil {
					out.Write(value.Label(source))
				}
			case *ast.CodeSpan:
				code := string(value.Text(source))
				code = strings.ReplaceAll(code, "\r\n", " ")
				code = strings.ReplaceAll(code, "\r", " ")
				code = strings.ReplaceAll(code, "\n", " ")
				result.CodeSpans = append(result.CodeSpans, code)
			}
		}
	}
	visit(parent)
	return out.String()
}
