package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

// Preserve the historical value's bytes, including unknown fields. Never let
// encoding/json's last-key-wins semantics disagree with SQLite's first key.
func legacyDeclaration(body []byte, excludeExited bool) ([]byte, int64, bool, error) {
	bad := errors.New("legacy_artifact_unclassified")
	if !utf8.Valid(body) {
		return nil, 0, false, bad
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return bad
		}
		token, err := d.Token()
		if err != nil {
			return bad
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				key, ok := token.(string)
				if err != nil || !ok || keys[key] {
					return bad
				}
				keys[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return bad
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return bad
			}
		default:
			return bad
		}
		return nil
	}
	if err := value(0); err != nil {
		return nil, 0, false, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, 0, false, bad
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return nil, 0, false, bad
	}
	var kind string
	if excludeExited && json.Unmarshal(top["kind"], &kind) == nil && kind == "exited" {
		return nil, 0, true, nil
	}
	raw, exists := top["artifact"]
	if !exists || bytes.Equal(raw, []byte("null")) {
		return nil, 0, true, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, 0, false, bad
	}
	n, err := strconv.ParseInt(string(bytes.TrimSpace(fields["size"])), 10, 64)
	if err != nil || n < 0 {
		return nil, 0, false, bad
	}
	return raw, n, false, nil
}
