package wire

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var supportedKeywords = map[string]bool{
	"$ref": true, "type": true, "const": true, "enum": true, "pattern": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"minItems": true, "maxItems": true, "uniqueItems": true, "required": true,
	"properties": true, "additionalProperties": true, "propertyNames": true,
	"maxProperties": true, "items": true, "allOf": true, "anyOf": true,
	"oneOf": true, "not": true, "if": true, "then": true, "else": true,
	"dependentRequired": true,
}

var ignoredAnnotations = map[string]bool{"$schema": true, "$id": true, "title": true, "description": true, "format": true, "$comment": true}

var jsonNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

type compiledPattern struct {
	match  *regexp.Regexp
	reject *regexp.Regexp
}

func (p compiledPattern) MatchString(s string) bool {
	return p.match.MatchString(s) && (p.reject == nil || !p.reject.MatchString(s))
}

func inspectSchema(v any, patterns map[string]compiledPattern) error {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for k := range m {
		if !supportedKeywords[k] && !ignoredAnnotations[k] && k != "$defs" {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	if p, ok := m["pattern"].(string); ok {
		if _, exists := patterns[p]; !exists {
			match, reject, err := compilePattern(p)
			if err != nil {
				return fmt.Errorf("invalid pattern: %w", err)
			}
			patterns[p] = compiledPattern{match: match, reject: reject}
		}
	}
	for _, keyword := range []string{"properties", "$defs"} {
		if children, ok := m[keyword].(map[string]any); ok {
			for _, key := range sortedKeys(children) {
				if err := inspectSchema(children[key], patterns); err != nil {
					return err
				}
			}
		}
	}
	for _, keyword := range []string{"items", "additionalProperties", "propertyNames", "not", "if", "then", "else"} {
		if child, ok := m[keyword]; ok && child != false {
			if err := inspectSchema(child, patterns); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if children, ok := m[keyword].([]any); ok {
			for _, child := range children {
				if err := inspectSchema(child, patterns); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func translatePattern(pattern string) (string, error) {
	const whitespace = `\x{9}-\x{D}\x{20}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
	var out strings.Builder
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '[' && strings.HasPrefix(pattern[i:], `[\S]`) {
			out.WriteString(`[^` + whitespace + `]`)
			i += len(`[\S]`) - 1
			continue
		}
		if c == '[' && strings.HasPrefix(pattern[i:], `[^\S]`) {
			out.WriteString(`[` + whitespace + `]`)
			i += len(`[^\S]`) - 1
			continue
		}
		if c == '[' {
			inClass = true
			out.WriteByte(c)
			continue
		}
		if c == ']' {
			inClass = false
			out.WriteByte(c)
			continue
		}
		if !inClass && c == '.' {
			out.WriteString(`[^\n\r\x{2028}\x{2029}]`)
			continue
		}
		if c != '\\' || i+1 >= len(pattern) {
			out.WriteByte(c)
			continue
		}
		i++
		next := pattern[i]
		if next == 'u' {
			if i+4 >= len(pattern) {
				return "", fmt.Errorf("short Unicode escape")
			}
			hex := pattern[i+1 : i+5]
			if _, err := strconv.ParseUint(hex, 16, 16); err != nil {
				return "", fmt.Errorf("invalid Unicode escape")
			}
			out.WriteString(`\x{` + hex + `}`)
			i += 4
			continue
		}
		if next == 's' || next == 'S' {
			if next == 'S' {
				if inClass {
					return "", fmt.Errorf("complement whitespace escape inside class")
				}
				out.WriteString(`[^` + whitespace + `]`)
			} else if inClass {
				out.WriteString(whitespace)
			} else {
				out.WriteString(`[` + whitespace + `]`)
			}
			continue
		}
		out.WriteByte('\\')
		out.WriteByte(next)
	}
	return out.String(), nil
}

func compilePattern(pattern string) (*regexp.Regexp, *regexp.Regexp, error) {
	const prefix = "^(?!"
	if strings.HasPrefix(pattern, prefix) {
		end, err := lookaheadEnd(pattern, len(prefix))
		if err != nil {
			return nil, nil, err
		}
		rejectPattern, err := translatePattern("^(?:" + pattern[len(prefix):end] + ")")
		if err != nil {
			return nil, nil, err
		}
		matchPattern, err := translatePattern("^" + pattern[end+1:])
		if err != nil {
			return nil, nil, err
		}
		match, err := regexp.Compile(matchPattern)
		if err != nil {
			return nil, nil, err
		}
		reject, err := regexp.Compile(rejectPattern)
		return match, reject, err
	}
	if strings.Contains(pattern, "(?=") || strings.Contains(pattern, "(?!") || strings.Contains(pattern, "(?<=") || strings.Contains(pattern, "(?<!") {
		return nil, nil, fmt.Errorf("unsupported lookaround")
	}
	translated, err := translatePattern(pattern)
	if err != nil {
		return nil, nil, err
	}
	match, err := regexp.Compile(translated)
	return match, nil, err
}

func lookaheadEnd(pattern string, start int) (int, error) {
	depth := 1
	inClass := false
	for i := start; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if !inClass {
				depth++
			}
		case ')':
			if !inClass {
				depth--
				if depth == 0 {
					return i, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("unterminated negative lookahead")
}

func validateSchema(doc schemaDocument, schema any, value any, def, pointer string) error {
	m, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(ref, prefix) {
			return validationError(def, pointer, "$ref")
		}
		target, ok := doc.defs[strings.TrimPrefix(ref, prefix)]
		if !ok {
			return validationError(def, pointer, "$ref")
		}
		if err := validateSchema(doc, target, value, def, pointer); err != nil {
			return err
		}
	}
	if typ, ok := m["type"]; ok && !matchesType(value, typ) {
		return validationError(def, pointer, "type")
	}
	if expected, ok := m["const"]; ok && !jsonEqual(value, expected) {
		return validationError(def, pointer, "const")
	}
	if choices, ok := m["enum"].([]any); ok {
		found := false
		for _, choice := range choices {
			if jsonEqual(value, choice) {
				found = true
			}
		}
		if !found {
			return validationError(def, pointer, "enum")
		}
	}
	if s, ok := value.(string); ok {
		if raw, ok := m["pattern"].(string); ok && !doc.patterns[raw].MatchString(s) {
			return validationError(def, pointer, "pattern")
		}
		if n, ok := intValue(m["minLength"]); ok && utf8.RuneCountInString(s) < n {
			return validationError(def, pointer, "minLength")
		}
		if n, ok := intValue(m["maxLength"]); ok && utf8.RuneCountInString(s) > n {
			return validationError(def, pointer, "maxLength")
		}
	}
	if n, ok := numberRat(value); ok {
		if bound, exists := m["minimum"]; exists {
			b, _ := numberRat(bound)
			if n.Cmp(b) < 0 {
				return validationError(def, pointer, "minimum")
			}
		}
		if bound, exists := m["maximum"]; exists {
			b, _ := numberRat(bound)
			if n.Cmp(b) > 0 {
				return validationError(def, pointer, "maximum")
			}
		}
	}
	if a, ok := value.([]any); ok {
		if n, ok := intValue(m["minItems"]); ok && len(a) < n {
			return validationError(def, pointer, "minItems")
		}
		if n, ok := intValue(m["maxItems"]); ok && len(a) > n {
			return validationError(def, pointer, "maxItems")
		}
		if m["uniqueItems"] == true {
			for i := 0; i < len(a); i++ {
				for j := 0; j < i; j++ {
					if jsonEqual(a[i], a[j]) {
						return validationError(def, pointer, "uniqueItems")
					}
				}
			}
		}
		if itemSchema, exists := m["items"]; exists {
			for i, item := range a {
				if err := validateSchema(doc, itemSchema, item, def, pointerChild(pointer, strconv.Itoa(i))); err != nil {
					return err
				}
			}
		}
	}
	if obj, ok := value.(map[string]any); ok {
		if n, ok := intValue(m["maxProperties"]); ok && len(obj) > n {
			return validationError(def, pointer, "maxProperties")
		}
		if required, ok := m["required"].([]any); ok {
			for _, item := range required {
				key := item.(string)
				if _, exists := obj[key]; !exists {
					return validationError(def, pointer, "required")
				}
			}
		}
		props, _ := m["properties"].(map[string]any)
		if names, exists := m["propertyNames"]; exists {
			for _, key := range sortedKeys(obj) {
				if err := validateSchema(doc, names, key, def, pointer); err != nil {
					return err
				}
			}
		}
		for _, key := range sortedKeys(obj) {
			if prop, exists := props[key]; exists {
				if err := validateSchema(doc, prop, obj[key], def, pointerChild(pointer, key)); err != nil {
					return err
				}
				continue
			}
			if extra, exists := m["additionalProperties"]; exists {
				if extra == false {
					return validationError(def, pointer, "additionalProperties")
				}
				if err := validateSchema(doc, extra, obj[key], def, pointerChild(pointer, key)); err != nil {
					return err
				}
			}
		}
		if deps, ok := m["dependentRequired"].(map[string]any); ok {
			for _, trigger := range sortedKeys(deps) {
				if _, present := obj[trigger]; present {
					required := deps[trigger].([]any)
					for _, req := range required {
						if _, present := obj[req.(string)]; !present {
							return validationError(def, pointer, "dependentRequired")
						}
					}
				}
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, ok := m[keyword].([]any)
		if !ok {
			continue
		}
		valid := 0
		for _, branch := range branches {
			if validateSchema(doc, branch, value, def, pointer) == nil {
				valid++
			} else if keyword == "allOf" {
				return validateSchema(doc, branch, value, def, pointer)
			}
		}
		if keyword == "allOf" {
			continue
		}
		if keyword == "anyOf" && valid == 0 {
			return validationError(def, pointer, keyword)
		}
		if keyword == "oneOf" && valid != 1 {
			return validationError(def, pointer, keyword)
		}
	}
	if neg, ok := m["not"]; ok && validateSchema(doc, neg, value, def, pointer) == nil {
		return validationError(def, pointer, "not")
	}
	if cond, exists := m["if"]; exists {
		branch := m["else"]
		if validateSchema(doc, cond, value, def, pointer) == nil {
			branch = m["then"]
		}
		if branch != nil {
			if err := validateSchema(doc, branch, value, def, pointer); err != nil {
				return err
			}
		}
	}
	return nil
}

func validationError(def, pointer, keyword string) error {
	return &ValidationError{Def: def, Pointer: pointer, Keyword: keyword}
}

func matchesType(value, typeSchema any) bool {
	if types, ok := typeSchema.([]any); ok {
		for _, typ := range types {
			if matchesType(value, typ) {
				return true
			}
		}
		return false
	}
	typ, _ := typeSchema.(string)
	switch typ {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numberRat(value)
		return ok
	case "integer":
		n, ok := numberRat(value)
		return ok && n.IsInt()
	default:
		return false
	}
}

func numberRat(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(string(n))
	return r, ok
}

func intValue(v any) (int, bool) {
	r, ok := numberRat(v)
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	return int(r.Num().Int64()), true
}

func jsonEqual(a, b any) bool {
	if ar, ok := numberRat(a); ok {
		br, bok := numberRat(b)
		return bok && ar.Cmp(br) == 0
	}
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, exists := bv[k]
			if !exists || !jsonEqual(x, y) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
