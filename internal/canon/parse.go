package canon

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// MaxSafeInteger is the largest magnitude a JSON number may have (2^53−1).
const MaxSafeInteger = 1<<53 - 1

// Limits bounds an input document.
type Limits struct {
	// MaxBytes limits the input size in bytes. Zero disables this limit.
	MaxBytes int
	// MaxDepth limits container nesting. Each object or array counts as one level,
	// with the outermost container at level one. This is the platform's
	// interpretation of the specification's "nesting depth 32". Zero disables
	// this limit.
	MaxDepth int
}

var (
	// MessageLimits bounds a complete public message or ordinary JSON document.
	MessageLimits = Limits{MaxBytes: 1 << 20, MaxDepth: 32}
	// ListingLimits bounds a public listing envelope.
	ListingLimits = Limits{MaxBytes: 2 << 20, MaxDepth: 32}
)

var (
	// ErrTooLarge reports an input larger than the configured byte limit.
	ErrTooLarge = errors.New("canonical JSON exceeds size limit")
	// ErrTooDeep reports a container nesting depth above the configured limit.
	ErrTooDeep = errors.New("canonical JSON exceeds nesting limit")
	// ErrSyntax reports input outside the strict RFC 8259 grammar.
	ErrSyntax = errors.New("invalid JSON syntax")
	// ErrDuplicateKey reports repeated object members after key unescaping.
	ErrDuplicateKey = errors.New("duplicate JSON object key")
	// ErrInvalidUnicode reports invalid UTF-8 or an unpaired surrogate escape.
	ErrInvalidUnicode = errors.New("invalid Unicode")
	// ErrNumberRange reports a parsed number outside the supported numeric range.
	ErrNumberRange = errors.New("JSON number out of range")
)

type parser struct {
	data []byte
	pos  int
	lim  Limits
}

// Parse parses strict RFC 8259 JSON into the canonical value model.
func Parse(data []byte, lim Limits) (any, error) {
	if lim.MaxBytes > 0 && len(data) > lim.MaxBytes {
		return nil, ErrTooLarge
	}
	if !utf8.Valid(data) {
		return nil, ErrInvalidUnicode
	}
	p := parser{data: data, lim: lim}
	p.space()
	value, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(data) {
		return nil, p.fail(ErrSyntax)
	}
	return value, nil
}

func (p *parser) fail(err error) error {
	return fmt.Errorf("%w at byte %d", err, p.pos)
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (any, error) {
	if p.pos >= len(p.data) {
		return nil, p.fail(ErrSyntax)
	}
	switch p.data[p.pos] {
	case '{':
		return p.object(depth + 1)
	case '[':
		return p.array(depth + 1)
	case '"':
		return p.string()
	case 't':
		return p.literal("true", true)
	case 'f':
		return p.literal("false", false)
	case 'n':
		return p.literal("null", nil)
	default:
		if p.data[p.pos] == '-' || p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return p.number()
		}
		return nil, p.fail(ErrSyntax)
	}
}

func (p *parser) checkDepth(depth int) error {
	if p.lim.MaxDepth > 0 && depth > p.lim.MaxDepth {
		return p.fail(ErrTooDeep)
	}
	return nil
}

func (p *parser) object(depth int) (any, error) {
	if err := p.checkDepth(depth); err != nil {
		return nil, err
	}
	p.pos++
	p.space()
	result := make(map[string]any)
	if p.take('}') {
		return result, nil
	}
	for {
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.fail(ErrSyntax)
		}
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, p.fail(ErrDuplicateKey)
		}
		p.space()
		if !p.take(':') {
			return nil, p.fail(ErrSyntax)
		}
		p.space()
		value, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		result[key] = value
		p.space()
		if p.take('}') {
			return result, nil
		}
		if !p.take(',') {
			return nil, p.fail(ErrSyntax)
		}
		p.space()
	}
}

func (p *parser) array(depth int) (any, error) {
	if err := p.checkDepth(depth); err != nil {
		return nil, err
	}
	p.pos++
	p.space()
	result := make([]any, 0)
	if p.take(']') {
		return result, nil
	}
	for {
		value, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		p.space()
		if p.take(']') {
			return result, nil
		}
		if !p.take(',') {
			return nil, p.fail(ErrSyntax)
		}
		p.space()
	}
}

func (p *parser) string() (string, error) {
	p.pos++
	start := p.pos
	result := make([]byte, 0)
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if b == '"' {
			result = append(result, p.data[start:p.pos]...)
			p.pos++
			return string(result), nil
		}
		if b < 0x20 {
			return "", p.fail(ErrSyntax)
		}
		if b != '\\' {
			p.pos++
			continue
		}
		result = append(result, p.data[start:p.pos]...)
		p.pos++
		if p.pos >= len(p.data) {
			return "", p.fail(ErrSyntax)
		}
		switch p.data[p.pos] {
		case '"', '\\', '/':
			result = append(result, p.data[p.pos])
			p.pos++
		case 'b':
			result = append(result, '\b')
			p.pos++
		case 'f':
			result = append(result, '\f')
			p.pos++
		case 'n':
			result = append(result, '\n')
			p.pos++
		case 'r':
			result = append(result, '\r')
			p.pos++
		case 't':
			result = append(result, '\t')
			p.pos++
		case 'u':
			unit, err := p.hexUnit()
			if err != nil {
				return "", err
			}
			r := rune(unit)
			if 0xD800 <= unit && unit <= 0xDBFF {
				if p.pos+2 > len(p.data) || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
					return "", p.fail(ErrInvalidUnicode)
				}
				p.pos++
				low, err := p.hexUnit()
				if err != nil {
					return "", err
				}
				if low < 0xDC00 || low > 0xDFFF {
					return "", p.fail(ErrInvalidUnicode)
				}
				r = 0x10000 + (rune(unit)-0xD800)*0x400 + (rune(low) - 0xDC00)
			} else if 0xDC00 <= unit && unit <= 0xDFFF {
				return "", p.fail(ErrInvalidUnicode)
			}
			result = utf8.AppendRune(result, r)
		default:
			return "", p.fail(ErrSyntax)
		}
		start = p.pos
	}
	return "", p.fail(ErrSyntax)
}

func (p *parser) hexUnit() (uint16, error) {
	if p.pos >= len(p.data) || p.data[p.pos] != 'u' || p.pos+5 > len(p.data) {
		return 0, p.fail(ErrSyntax)
	}
	decoded := make([]byte, 2)
	if _, err := hex.Decode(decoded, p.data[p.pos+1:p.pos+5]); err != nil {
		return 0, p.fail(ErrSyntax)
	}
	p.pos += 5
	return uint16(decoded[0])<<8 | uint16(decoded[1]), nil
}

func (p *parser) number() (any, error) {
	start := p.pos
	p.take('-')
	if p.take('0') {
		if p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return nil, p.fail(ErrSyntax)
		}
	} else {
		if p.pos >= len(p.data) || p.data[p.pos] < '1' || p.data[p.pos] > '9' {
			return nil, p.fail(ErrSyntax)
		}
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.take('.') {
		fractionStart := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == fractionStart {
			return nil, p.fail(ErrSyntax)
		}
	}
	if p.take('e') || p.take('E') {
		if !p.take('+') {
			p.take('-')
		}
		exponentStart := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == exponentStart {
			return nil, p.fail(ErrSyntax)
		}
	}
	lexical := string(p.data[start:p.pos])
	f, _ := strconv.ParseFloat(lexical, 64)
	if math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) > MaxSafeInteger {
		return nil, p.fail(ErrNumberRange)
	}
	return json.Number(lexical), nil
}

func (p *parser) literal(text string, value any) (any, error) {
	if len(p.data)-p.pos < len(text) || string(p.data[p.pos:p.pos+len(text)]) != text {
		return nil, p.fail(ErrSyntax)
	}
	p.pos += len(text)
	return value, nil
}

func (p *parser) take(b byte) bool {
	if p.pos < len(p.data) && p.data[p.pos] == b {
		p.pos++
		return true
	}
	return false
}
