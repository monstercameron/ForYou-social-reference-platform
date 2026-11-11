package content

import (
	"bytes"
	stdhtml "html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var (
	mdIDPattern       = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	mdURLLabelPattern = regexp.MustCompile(`(?i)^(?:https?://|www\.|[a-z0-9-]+(?:\.[a-z0-9-]+)+(?:\:\d+)?(?:/|$))`)
)

type mdParser struct {
	goldmark.Markdown
	titles map[ast.Node]bool
}

// mdLinkParser wraps goldmark's link parser and records every link or image written with a title, including an
// empty one that goldmark reports as a nil Title (docs/02-protocol.md "Destination validation").
type mdLinkParser struct {
	parser.InlineParser
	titles map[ast.Node]bool
}

func mdNewParser() mdParser {
	titles := make(map[ast.Node]bool)
	md := goldmark.New(goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(mdInlineParsers(titles)...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)))
	return mdParser{Markdown: md, titles: titles}
}

func mdInlineParsers(titles map[ast.Node]bool) []util.PrioritizedValue {
	parsers := parser.DefaultInlineParsers()
	for i, prioritized := range parsers {
		if _, ok := prioritized.Value.(interface {
			CloseBlock(ast.Node, text.Reader, parser.Context)
		}); ok && bytes.Equal(prioritized.Value.(parser.InlineParser).Trigger(), []byte{'!', '[', ']'}) {
			parsers[i] = util.Prioritized(mdLinkParser{InlineParser: prioritized.Value.(parser.InlineParser), titles: titles}, prioritized.Priority)
		}
	}
	return parsers
}

func (p mdLinkParser) Parse(parent ast.Node, reader text.Reader, context parser.Context) ast.Node {
	if reader.Peek() != ']' {
		return p.InlineParser.Parse(parent, reader, context)
	}
	beforeLine, before := reader.Position()
	node := p.InlineParser.Parse(parent, reader, context)
	if node == nil {
		return nil
	}
	afterLine, after := reader.Position()
	consumed, spanOK := mdConsumed(reader, beforeLine, before, afterLine, after)
	titled := !spanOK
	if link, ok := node.(*ast.Link); ok {
		titled = titled || link.Title != nil
	} else if image, ok := node.(*ast.Image); ok {
		titled = titled || image.Title != nil
	} else {
		titled = true
	}
	if !titled {
		if close := bytes.LastIndexByte(consumed, ')'); close < 0 {
			titled = true
		} else if destinationEnd := mdDestinationEnd(consumed); destinationEnd < 0 || strings.TrimSpace(string(consumed[destinationEnd:close])) != "" {
			titled = true
		}
	}
	if titled {
		p.titles[node] = true
	}
	return node
}

func mdConsumed(reader text.Reader, startLine int, start text.Segment, endLine int, end text.Segment) ([]byte, bool) {
	if startLine > endLine || start.Start < 0 || end.Start < start.Start {
		return nil, false
	}
	endPositionLine, endPosition := reader.Position()
	reader.SetPosition(startLine, start)
	defer reader.SetPosition(endPositionLine, endPosition)
	var consumed []byte
	for lineNumber := startLine; lineNumber <= endLine; lineNumber++ {
		line, segment := reader.PeekLine()
		if line == nil || segment.Start < 0 {
			return nil, false
		}
		if lineNumber == endLine {
			count := end.Start - segment.Start
			if count < 0 || count > len(line) {
				return nil, false
			}
			consumed = append(consumed, line[:count]...)
			break
		}
		consumed = append(consumed, line...)
		reader.AdvanceLine()
	}
	return consumed, true
}

func (p mdLinkParser) CloseBlock(parent ast.Node, reader text.Reader, context parser.Context) {
	if closeBlocker, ok := p.InlineParser.(parser.CloseBlocker); ok {
		closeBlocker.CloseBlock(parent, reader, context)
	}
}

func mdDestinationEnd(syntax []byte) int {
	if len(syntax) == 0 || syntax[0] != ']' {
		return -1
	}
	i := 1
	for i < len(syntax) && (syntax[i] == ' ' || syntax[i] == '\t' || syntax[i] == '\n' || syntax[i] == '\r') {
		i++
	}
	if i >= len(syntax) || syntax[i] != '(' {
		return -1
	}
	i++
	for i < len(syntax) && (syntax[i] == ' ' || syntax[i] == '\t' || syntax[i] == '\n' || syntax[i] == '\r') {
		i++
	}
	if i < len(syntax) && syntax[i] == '<' {
		for i++; i < len(syntax); i++ {
			if syntax[i] == '>' {
				return i + 1
			}
			if syntax[i] == '\\' && i+1 < len(syntax) {
				i++
			}
		}
		return -1
	}
	depth := 0
	for ; i < len(syntax); i++ {
		switch syntax[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		case ' ', '\t', '\n', '\r':
			return i
		}
	}
	return -1
}

// mdParsedDestination applies CommonMark backslash escapes and character references in one left-to-right pass.
func mdParsedDestination(destination []byte) string {
	var decoded strings.Builder
	for i := 0; i < len(destination); {
		c := destination[i]
		if c == '\\' && i+1 < len(destination) && util.IsPunct(destination[i+1]) {
			decoded.WriteByte(destination[i+1])
			i += 2
			continue
		}
		if c == '&' {
			if value, n := mdCharacterReference(destination[i:]); n > 0 {
				decoded.WriteString(value)
				i += n
				continue
			}
		}
		decoded.WriteByte(c)
		i++
	}
	return decoded.String()
}

// mdMaxReferenceLength bounds the search for ';': the longest HTML5 entity name has 31 characters.
const mdMaxReferenceLength = 34

// mdCharacterReference decodes the entity or numeric character reference that starts s, as CommonMark 0.31.2
// section 2.5 defines it (decimal 1-7 digits, hexadecimal 1-6 digits, code point 0 or invalid becomes U+FFFD),
// and returns the decoded text and the number of bytes consumed, or 0 when s does not start with a reference.
func mdCharacterReference(s []byte) (string, int) {
	end := bytes.IndexByte(s[:min(len(s), mdMaxReferenceLength)], ';')
	if end < 2 {
		return "", 0
	}
	body := s[1:end]
	if body[0] != '#' {
		if entity, ok := util.LookUpHTML5EntityByName(string(body)); ok {
			return string(entity.Characters), end + 1
		}
		return "", 0
	}
	digits, base, maxDigits := body[1:], 10, 7
	if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
		digits, base, maxDigits = digits[1:], 16, 6
	}
	if len(digits) == 0 || len(digits) > maxDigits {
		return "", 0
	}
	value, err := strconv.ParseUint(string(digits), base, 32)
	if err != nil {
		return "", 0
	}
	r := rune(value)
	if r == 0 || !utf8.ValidRune(r) {
		r = utf8.RuneError
	}
	return string(r), end + 1
}

func mdLabel(node ast.Node, source []byte) string {
	var out strings.Builder
	var visit func(ast.Node)
	visit = func(current ast.Node) {
		for child := current.FirstChild(); child != nil; child = child.NextSibling() {
			switch value := child.(type) {
			case *ast.Text:
				out.WriteString(stdhtml.UnescapeString(mdWriteText(value.Text(source))))
				if value.SoftLineBreak() || value.HardLineBreak() {
					out.WriteByte('\n')
				}
			case *ast.String:
				out.Write(value.Value)
			case *ast.CodeSpan:
				code := strings.ReplaceAll(string(value.Text(source)), "\r\n", " ")
				code = strings.ReplaceAll(code, "\r", " ")
				code = strings.ReplaceAll(code, "\n", " ")
				out.WriteString(code)
			case *ast.AutoLink:
				out.Write(value.Label(source))
			case *ast.RawHTML:
				for i := range value.Segments.Len() {
					segment := value.Segments.At(i)
					out.Write(segment.Value(source))
				}
			default:
				visit(child)
			}
		}
	}
	visit(node)
	return out.String()
}
