package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"unicode/utf8"
)

// Unlike the bridge DTOs, owner messages carry a signed RawMessage union.
// Scan every opaque value for duplicate keys, while enforcing exact field
// names on typed objects. Individual handlers then decode the selected body.
func DecodeOwnerRuntimeStrict(raw []byte, dst any) error {
	value := reflect.ValueOf(dst)
	if len(raw) == 0 || len(raw) > OriginalOwnerEnvelopeLimit || !utf8.Valid(raw) || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return ownerInvalid()
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	if ownerRuntimeScan(scan, value.Elem().Type(), 0) != nil {
		return ownerInvalid()
	}
	if _, err := scan.Token(); err != io.EOF {
		return ownerInvalid()
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(dst) != nil || decode.Decode(new(any)) != io.EOF {
		return ownerInvalid()
	}
	return nil
}
func ownerRuntimeScan(dec *json.Decoder, target reflect.Type, depth int) error {
	if depth > 64 {
		return ownerInvalid()
	}
	if target != nil {
		for target.Kind() == reflect.Pointer {
			target = target.Elem()
		}
		if target == reflect.TypeOf(json.RawMessage{}) {
			target = nil
		}
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		var fields map[string]reflect.Type
		if target != nil {
			if target.Kind() != reflect.Struct {
				return ownerInvalid()
			}
			fields = bridgeExactFields(target)
		}
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ownerInvalid()
			}
			seen[name] = true
			var child reflect.Type
			if target != nil {
				var exists bool
				child, exists = fields[name]
				if !exists {
					return ownerInvalid()
				}
			}
			if err = ownerRuntimeScan(dec, child, depth+1); err != nil {
				return err
			}
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim('}') {
			return ownerInvalid()
		}
	case '[':
		var child reflect.Type
		if target != nil {
			if target.Kind() != reflect.Slice && target.Kind() != reflect.Array {
				return ownerInvalid()
			}
			child = target.Elem()
		}
		for dec.More() {
			if err = ownerRuntimeScan(dec, child, depth+1); err != nil {
				return err
			}
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim(']') {
			return ownerInvalid()
		}
	default:
		return ownerInvalid()
	}
	return nil
}
func decodeOwnerRuntime(raw []byte, dst any) error {
	if err := DecodeOwnerRuntimeStrict(raw, dst); err != nil {
		return err
	}
	if !ownerRequiredFields(raw, reflect.TypeOf(dst).Elem()) {
		return ownerInvalid()
	}
	return nil
}
func ownerCanonicalBody(raw json.RawMessage) (json.RawMessage, error) {
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, ownerInvalid()
	}
	return body, nil
}

func ownerOpaqueBodyValid(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(raw) {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if ownerRuntimeScan(dec, nil, 0) != nil {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}
