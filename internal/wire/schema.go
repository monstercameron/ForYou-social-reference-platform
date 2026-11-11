package wire

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Specification release 1.0 sources: ../ForYou-Social-Ecosystem-Specification/schemas/launch.schema.json and ../ForYou-Social-Ecosystem-Specification/schemas/operations.schema.json.
//
//go:embed schemas/launch.schema.json
var launchSchemaJSON []byte

//go:embed schemas/operations.schema.json
var operationsSchemaJSON []byte

type schemaDocument struct {
	defs     map[string]any
	patterns map[string]compiledPattern
}

var schemaState struct {
	sync.Once
	launch, operations schemaDocument
	err                error
}

// ValidationError identifies the first structural schema violation.
type ValidationError struct {
	// Def is the requested definition and optional pointer suffix.
	Def string
	// Pointer is the RFC 6901 location in the instance.
	Pointer string
	// Keyword is the schema keyword that rejected the instance.
	Keyword string
}

// Error returns a value-free description of the schema violation.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("wire: %s violates %s at %s", e.Def, e.Keyword, e.Pointer)
}

// Check validates v, a JSON value in internal/canon's value model, against the named
// $defs entry, optionally followed by a JSON Pointer into that entry.
func Check(def string, v any) error {
	if err := checkValueModel(v); err != nil {
		return err
	}
	loadSchemas()
	if schemaState.err != nil {
		return fmt.Errorf("wire schema load: %w", schemaState.err)
	}
	name, fragment, _ := strings.Cut(def, "/")
	doc := schemaState.launch
	root, ok := doc.defs[name]
	if !ok {
		doc = schemaState.operations
		root, ok = doc.defs[name]
	}
	if !ok {
		return fmt.Errorf("wire: unknown definition %q", name)
	}
	if fragment != "" {
		var err error
		root, err = resolveDefinitionPointer(root, strings.Split(fragment, "/"))
		if err != nil {
			return fmt.Errorf("wire: invalid definition pointer")
		}
	}
	return validateSchema(doc, root, v, def, "")
}

func checkValueModel(v any) error {
	switch x := v.(type) {
	case nil, string, bool:
		return nil
	case json.Number:
		if !jsonNumberPattern.MatchString(string(x)) {
			return fmt.Errorf("wire: invalid JSON number")
		}
		return nil
	case []any:
		for _, item := range x {
			if err := checkValueModel(item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, key := range sortedKeys(x) {
			if err := checkValueModel(x[key]); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("wire: unsupported JSON value type %T", v)
	}
}

func loadSchemas() {
	schemaState.Do(func() {
		schemaState.launch, schemaState.err = parseSchema(launchSchemaJSON)
		if schemaState.err != nil {
			return
		}
		schemaState.operations, schemaState.err = parseSchema(operationsSchemaJSON)
	})
}

func parseSchema(data []byte) (schemaDocument, error) {
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return schemaDocument{}, err
	}
	defs, ok := raw["$defs"].(map[string]any)
	if !ok {
		return schemaDocument{}, fmt.Errorf("missing $defs")
	}
	if err := checkSchemaReferences(raw, defs); err != nil {
		return schemaDocument{}, err
	}
	patterns := map[string]compiledPattern{}
	if err := inspectSchema(raw, patterns); err != nil {
		return schemaDocument{}, err
	}
	return schemaDocument{defs: defs, patterns: patterns}, nil
}

func checkSchemaReferences(v any, defs map[string]any) error {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			const prefix = "#/$defs/"
			if !strings.HasPrefix(ref, prefix) || defs[strings.TrimPrefix(ref, prefix)] == nil {
				return fmt.Errorf("invalid local schema reference")
			}
		}
		for _, child := range x {
			if err := checkSchemaReferences(child, defs); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := checkSchemaReferences(child, defs); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveDefinitionPointer(root any, parts []string) (any, error) {
	for _, p := range parts {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		switch x := root.(type) {
		case map[string]any:
			var ok bool
			root, ok = x[p]
			if !ok {
				return nil, fmt.Errorf("missing pointer")
			}
		default:
			return nil, fmt.Errorf("invalid pointer")
		}
	}
	return root, nil
}

func pointerChild(pointer, key string) string {
	key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	return pointer + "/" + key
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
