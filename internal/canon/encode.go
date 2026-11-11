package canon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var jsonNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// Marshal encodes a canonical value-model tree using RFC 8785 JCS. A json.Number
// outside ±MaxSafeInteger is outside the value model and returns ErrNumberRange.
func Marshal(v any) ([]byte, error) {
	var output bytes.Buffer
	if err := writeValue(&output, v); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// Canonicalize parses strict JSON under lim and returns its JCS encoding.
func Canonicalize(data []byte, lim Limits) ([]byte, error) {
	value, err := Parse(data, lim)
	if err != nil {
		return nil, err
	}
	return Marshal(value)
}

// Encode canonicalizes an arbitrary Go value through its JSON representation.
func Encode(v any) ([]byte, error) {
	if err := validGoStrings(reflect.ValueOf(v), make(map[visit]struct{})); err != nil {
		return nil, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	value, err := Parse(data, Limits{MaxDepth: 32})
	if err != nil {
		return nil, err
	}
	return Marshal(value)
}

// EncodeWithout encodes v after removing the named top-level object members.
func EncodeWithout(v any, omit ...string) ([]byte, error) {
	encoded, err := Encode(v)
	if err != nil {
		return nil, err
	}
	value, err := Parse(encoded, Limits{MaxDepth: 32})
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top-level value must be an object")
	}
	for _, key := range omit {
		delete(object, key)
	}
	return Marshal(object)
}

type visit struct {
	typ reflect.Type
	ptr uintptr
	len int
}

func validGoStrings(value reflect.Value, seen map[visit]struct{}) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			key := visit{typ: value.Type(), ptr: value.Pointer()}
			if _, ok := seen[key]; ok {
				return nil
			}
			seen[key] = struct{}{}
		}
		return validGoStrings(value.Elem(), seen)
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return ErrInvalidUnicode
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		key := visit{typ: value.Type(), ptr: value.Pointer()}
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		iter := value.MapRange()
		for iter.Next() {
			if iter.Key().Kind() == reflect.String && !utf8.ValidString(iter.Key().String()) {
				return ErrInvalidUnicode
			}
			if err := validGoStrings(iter.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		key := visit{typ: value.Type(), ptr: value.Pointer(), len: value.Len()}
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		fallthrough
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validGoStrings(value.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if err := validGoStrings(value.Field(i), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeValue(output *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(value))
	case string:
		if !utf8.ValidString(value) {
			return ErrInvalidUnicode
		}
		writeString(output, value)
	case json.Number:
		encoded, err := canonicalNumber(string(value))
		if err != nil {
			return err
		}
		output.WriteString(encoded)
	case []any:
		output.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				output.WriteByte(',')
			}
			if err := writeValue(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			if !utf8.ValidString(key) {
				return ErrInvalidUnicode
			}
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		output.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				output.WriteByte(',')
			}
			writeString(output, key)
			output.WriteByte(':')
			if err := writeValue(output, value[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func utf16Less(a, b string) bool {
	left := utf16.Encode([]rune(a))
	right := utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

func writeString(output *bytes.Buffer, value string) {
	output.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			output.WriteString(`\"`)
		case '\\':
			output.WriteString(`\\`)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if r < 0x20 {
				output.WriteString(`\u00`)
				const digits = "0123456789abcdef"
				output.WriteByte(digits[byte(r)>>4])
				output.WriteByte(digits[byte(r)&0x0f])
			} else {
				output.WriteRune(r)
			}
		}
	}
	output.WriteByte('"')
}

func canonicalNumber(value string) (string, error) {
	if !jsonNumberPattern.MatchString(value) {
		return "", ErrSyntax
	}
	f, _ := strconv.ParseFloat(value, 64)
	if math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) > MaxSafeInteger {
		return "", ErrNumberRange
	}
	if f == 0 {
		return "0", nil
	}
	scientific := strconv.FormatFloat(f, 'e', -1, 64)
	signPrefix := ""
	if strings.HasPrefix(scientific, "-") {
		signPrefix = "-"
		scientific = scientific[1:]
	}
	parts := strings.SplitN(scientific, "e", 2)
	exponent, _ := strconv.Atoi(parts[1])
	digits := strings.ReplaceAll(parts[0], ".", "")
	n := exponent + 1
	switch {
	case len(digits) <= n && n <= 21:
		return signPrefix + digits + strings.Repeat("0", n-len(digits)), nil
	case 0 < n && n <= 21:
		return signPrefix + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return signPrefix + "0." + strings.Repeat("0", -n) + digits, nil
	default:
		mantissa := digits[:1]
		if len(digits) > 1 {
			mantissa += "." + digits[1:]
		}
		sign := "+"
		if n-1 < 0 {
			sign = ""
		}
		return signPrefix + mantissa + "e" + sign + strconv.Itoa(n-1), nil
	}
}
