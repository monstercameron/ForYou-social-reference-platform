package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"foryou.local/foryou/internal/canon"
)

// Decode parses and validates a message-sized wire object into v using canon.MessageLimits.
func Decode(data []byte, def string, v any) error {
	return DecodeLimits(data, canon.MessageLimits, def, v)
}

// DecodeLimits parses, validates, and decodes a wire object under lim. v must be a non-nil pointer and is left unchanged on any error.
func DecodeLimits(data []byte, lim canon.Limits, def string, v any) error {
	tree, err := canon.Parse(data, lim)
	if err != nil {
		return fmt.Errorf("wire decode parse: %w", err)
	}
	if err := Check(def, tree); err != nil {
		return err
	}
	target := reflect.ValueOf(v)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("wire decode target must be a non-nil pointer")
	}
	value := reflect.New(target.Elem().Type())
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value.Interface()); err != nil {
		return fmt.Errorf("wire decode value: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("wire decode: trailing JSON value")
		}
		return fmt.Errorf("wire decode trailer: %w", err)
	}
	target.Elem().Set(value.Elem())
	return nil
}

// Encode returns RFC 8785 canonical bytes and refuses a value that does not satisfy def.
func Encode(def string, v any) ([]byte, error) {
	data, err := canon.Encode(v)
	if err != nil {
		return nil, fmt.Errorf("wire encode canonicalize: %w", err)
	}
	// Responses may include 2 MiB listing pages, so the caller owns the body size limit.
	tree, err := canon.Parse(data, canon.Limits{MaxDepth: canon.MessageLimits.MaxDepth})
	if err != nil {
		return nil, fmt.Errorf("wire encode parse: %w", err)
	}
	if err := Check(def, tree); err != nil {
		return nil, err
	}
	return data, nil
}
