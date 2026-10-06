package codexsession

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The only schemas this private matcher can load are the immutable M3 extract.
// It is not a user/schema API. Policy, IDs and phase authorization are separate.
//
//go:embed shapes.json
var pinnedShapes []byte

const maxShapeDepth = 64
const maxShapeWork = 8 << 20

var ErrShapeLimit = errors.New("codexsession: shape_limit")

type shapeBudget struct {
	work int
	err  error
}

func (b *shapeBudget) spend(n int) bool {
	if n < 0 || n > maxShapeWork-b.work {
		b.err = ErrShapeLimit
		return false
	}
	b.work += n
	return b.err == nil
}

func strictValue(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	b := &shapeBudget{}
	var value func(int) (any, error)
	value = func(depth int) (any, error) {
		if depth > maxShapeDepth || !b.spend(1) {
			return nil, ErrShapeLimit
		}
		t, err := d.Token()
		if err != nil {
			return nil, ErrProtocol
		}
		switch t {
		case json.Delim('{'):
			object := map[string]any{}
			for d.More() {
				t, err := d.Token()
				key, ok := t.(string)
				if err != nil || !ok {
					return nil, ErrProtocol
				}
				if _, exists := object[key]; exists {
					return nil, ErrProtocol
				}
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				object[key] = v
			}
			if t, err = d.Token(); err != nil || t != json.Delim('}') {
				return nil, ErrProtocol
			}
			return object, nil
		case json.Delim('['):
			array := []any{}
			for d.More() {
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, v)
			}
			if t, err = d.Token(); err != nil || t != json.Delim(']') {
				return nil, ErrProtocol
			}
			return array, nil
		default:
			if _, delimiter := t.(json.Delim); delimiter {
				return nil, ErrProtocol
			}
			return t, nil
		}
	}
	v, err := value(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrProtocol
	}
	return v, nil
}

// References and unions are expanded at the same object level before checking
// unknown fields. Partial allOf branches cannot reject another branch's keys;
// a selected union cannot authorize keys from an unselected alternative.
type nativeShapes struct {
	schemas, definitions map[string]any
}

func loadShapes() (*nativeShapes, error) {
	value, err := strictValue(pinnedShapes)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrProtocol
	}
	schemas, a := object["schemas"].(map[string]any)
	definitions, b := object["definitions"].(map[string]any)
	if !a || !b {
		return nil, ErrProtocol
	}
	return &nativeShapes{schemas, definitions}, nil
}

func (s *nativeShapes) object(name string, raw json.RawMessage) (map[string]any, error) {
	schema, exists := s.schemas[name]
	if !exists {
		return nil, ErrProtocol
	}
	value, err := strictValue(raw)
	if err != nil {
		return nil, err
	}
	b := &shapeBudget{}
	if !s.match([]any{schema}, value, 0, b) {
		if b.err != nil {
			return nil, b.err
		}
		return nil, ErrProtocol
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrProtocol
	}
	return object, nil
}

func (s *nativeShapes) match(parts []any, value any, depth int, b *shapeBudget) bool {
	if depth > maxShapeDepth || !b.spend(len(parts)+1) {
		b.err = ErrShapeLimit
		return false
	}
	for i, part := range parts {
		if truth, ok := part.(bool); ok {
			if !truth {
				return false
			}
			continue
		}
		node, ok := part.(map[string]any)
		if !ok {
			return false
		}
		for key := range node {
			switch key {
			case "$ref", "additionalProperties", "allOf", "anyOf", "enum", "format", "items", "minLength", "minimum", "oneOf", "properties", "required", "type":
			default:
				return false
			}
		}
		if ref, ok := node["$ref"].(string); ok {
			name, valid := strings.CutPrefix(ref, "#/definitions/")
			target, exists := s.definitions[name]
			if !valid || !exists {
				return false
			}
			rest := without(node, "$ref")
			return s.match(replace(parts, i, rest, target), value, depth+1, b)
		}
		if children, ok := node["allOf"].([]any); ok {
			return s.match(replace(parts, i, append([]any{without(node, "allOf")}, children...)...), value, depth+1, b)
		}
		for _, union := range []string{"anyOf", "oneOf"} {
			if children, ok := node[union].([]any); ok {
				matched := 0
				for _, child := range children {
					if s.match(replace(parts, i, without(node, union), child), value, depth+1, b) {
						matched++
						if union == "anyOf" {
							return true
						}
					}
					if b.err != nil {
						return false
					}
				}
				return matched == 1
			}
		}
	}
	known := map[string][]any{}
	var extras []any
	objectShape, denyExtras := false, false
	for _, part := range parts {
		node, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if !atomicShape(node, value, b) {
			return false
		}
		if properties, ok := node["properties"].(map[string]any); ok {
			objectShape = true
			for key, child := range properties {
				known[key] = append(known[key], child)
			}
		}
		if node["type"] == "object" {
			objectShape = true
		}
		if extra, exists := node["additionalProperties"]; exists {
			if extra == false {
				denyExtras = true
			} else {
				extras = append(extras, extra)
			}
		}
		if array, ok := value.([]any); ok {
			if item, exists := node["items"]; exists {
				for _, entry := range array {
					if !s.match([]any{item}, entry, depth+1, b) {
						return false
					}
				}
			}
		}
	}
	if object, ok := value.(map[string]any); ok && objectShape {
		for key, child := range object {
			constraints := known[key]
			if constraints == nil {
				if denyExtras || len(extras) == 0 {
					return false
				}
				constraints = extras
			}
			if !b.spend(len(key)+1) || !s.match(constraints, child, depth+1, b) {
				return false
			}
		}
	}
	return b.err == nil
}

func replace(parts []any, index int, insert ...any) []any {
	result := append([]any{}, parts[:index]...)
	result = append(result, insert...)
	return append(result, parts[index+1:]...)
}

func without(node map[string]any, key string) map[string]any {
	copy := make(map[string]any, len(node)-1)
	for name, value := range node {
		if name != key {
			copy[name] = value
		}
	}
	return copy
}

func atomicShape(node map[string]any, value any, b *shapeBudget) bool {
	if kind, exists := node["type"]; exists {
		types := []any{kind}
		if array, ok := kind.([]any); ok {
			types = array
		}
		matched := false
		for _, name := range types {
			switch name {
			case "null":
				matched = matched || value == nil
			case "object":
				_, ok := value.(map[string]any)
				matched = matched || ok
			case "array":
				_, ok := value.([]any)
				matched = matched || ok
			case "boolean":
				_, ok := value.(bool)
				matched = matched || ok
			case "string":
				_, ok := value.(string)
				matched = matched || ok
			case "integer":
				n, ok := value.(json.Number)
				// Native integer formats do not accept decimal/exponent tokens.
				matched = matched || ok && n != "" && !strings.ContainsAny(string(n), ".eE")
			case "number":
				_, ok := value.(json.Number)
				matched = matched || ok
			default:
				return false
			}
		}
		if !matched {
			return false
		}
	}
	if choices, ok := node["enum"].([]any); ok {
		found := false
		for _, choice := range choices {
			found = found || reflect.DeepEqual(choice, value)
		}
		if !found {
			return false
		}
	}
	if object, ok := value.(map[string]any); ok {
		if required, ok := node["required"].([]any); ok {
			for _, field := range required {
				name, ok := field.(string)
				if !ok {
					return false
				}
				if _, exists := object[name]; !exists {
					return false
				}
			}
		}
	}
	if text, ok := value.(string); ok {
		if minimum, ok := node["minLength"].(json.Number); ok {
			n, err := minimum.Int64()
			if err != nil || !b.spend(len(text)) || int64(utf8.RuneCountInString(text)) < n {
				return false
			}
		}
	}
	if number, ok := value.(json.Number); ok {
		if !b.spend(len(number)) {
			return false
		}
		// Bound exact rational construction before allocating powers of ten.
		// These are private parser resource bounds, not task text quotas.
		if len(number) > 1024 {
			b.err = ErrShapeLimit
			return false
		}
		if pos := strings.IndexAny(string(number), "eE"); pos >= 0 {
			exponent, err := strconv.ParseInt(string(number)[pos+1:], 10, 32)
			if err != nil || exponent < -1024 || exponent > 1024 {
				b.err = ErrShapeLimit
				return false
			}
		}
		if minimum, ok := node["minimum"].(json.Number); ok {
			a, aok := new(big.Rat).SetString(string(number))
			c, cok := new(big.Rat).SetString(string(minimum))
			if !aok || !cok || a.Cmp(c) < 0 {
				return false
			}
		}
		if format, ok := node["format"].(string); ok {
			var err error
			switch format {
			case "int32":
				_, err = strconv.ParseInt(string(number), 10, 32)
			case "int64":
				_, err = strconv.ParseInt(string(number), 10, 64)
			case "uint":
				_, err = strconv.ParseUint(string(number), 10, 64)
			case "uint16":
				_, err = strconv.ParseUint(string(number), 10, 16)
			case "uint32":
				_, err = strconv.ParseUint(string(number), 10, 32)
			default:
				return false
			}
			if err != nil {
				return false
			}
		}
	}
	return b.err == nil
}
