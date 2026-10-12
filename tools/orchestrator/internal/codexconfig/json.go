package codexconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Decode tokens so duplicate keys cannot be hidden by encoding/json's normal
// last-value-wins map decoding. Numbers retain their exact decimal spelling.
func decodeObject(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) || !validUnicodeEscapes(raw) {
		return nil, ErrInvalidFormat
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return nil, ErrInvalidFormat
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidFormat
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidFormat
	}
	return object, nil
}

// The standard decoder silently repairs unpaired UTF-16 escapes to U+FFFD.
// Reject that lossy representation before it can match a preserved scalar.
func validUnicodeEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue // Includes an escaped backslash, not a Unicode escape.
		}
		if i+5 > len(raw) {
			return false
		}
		code, ok := hexCode(raw[i+1 : i+5])
		if !ok || code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+11 > len(raw) || raw[i+5] != '\\' || raw[i+6] != 'u' {
				return false
			}
			low, ok := hexCode(raw[i+7 : i+11])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 10
		} else {
			i += 4
		}
	}
	return true
}

func hexCode(raw []byte) (uint16, bool) {
	var code uint16
	for _, digit := range raw {
		code <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			code |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			code |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			code |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return code, true
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalidFormat
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrInvalidFormat
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, ErrInvalidFormat
			}
			key, ok := token.(string)
			if !ok {
				return nil, ErrInvalidFormat
			}
			if _, duplicate := object[key]; duplicate {
				return nil, ErrInvalidFormat
			}
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return nil, ErrInvalidFormat
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return nil, ErrInvalidFormat
		}
		return array, nil
	default:
		return nil, ErrInvalidFormat
	}
}
