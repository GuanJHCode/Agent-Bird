package gitopsworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Journals contain both an opaque value envelope and typed file inventories.
// The bridge's struct-only scanner deliberately supports neither. Keep this
// bounded journal decoder local; exact typed keys and duplicate checks apply
// through maps, while an envelope value is typed again by its record consumer.
func decodeJournalStrict(raw []byte, dst any) error {
	target := reflect.TypeOf(dst)
	if !utf8.Valid(raw) || target == nil || target.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() {
		return errors.New("gitops_journal_invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJournalValue(dec, target.Elem()); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("gitops_journal_invalid")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func scanJournalValue(dec *json.Decoder, target reflect.Type) error {
	bad := errors.New("gitops_journal_invalid")
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == reflect.TypeFor[json.RawMessage]() {
		target = nil
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	var close json.Delim
	switch delim {
	case '{':
		close = '}'
		var fields map[string]reflect.Type
		var element reflect.Type
		if target != nil {
			switch target.Kind() {
			case reflect.Struct:
				fields = make(map[string]reflect.Type)
				for i := 0; i < target.NumField(); i++ {
					field := target.Field(i)
					if field.PkgPath != "" {
						continue
					}
					name := strings.Split(field.Tag.Get("json"), ",")[0]
					if name == "-" {
						continue
					}
					if name == "" {
						name = field.Name
					}
					fields[name] = field.Type
				}
			case reflect.Map:
				if target.Key().Kind() != reflect.String {
					return bad
				}
				element = target.Elem()
			default:
				return bad
			}
		}
		seen := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return bad
			}
			seen[key] = true
			valueType := element
			if fields != nil {
				valueType, ok = fields[key]
				if !ok {
					return bad
				}
			}
			if err := scanJournalValue(dec, valueType); err != nil {
				return err
			}
		}
	case '[':
		close = ']'
		var element reflect.Type
		if target != nil {
			if target.Kind() != reflect.Slice && target.Kind() != reflect.Array {
				return bad
			}
			element = target.Elem()
		}
		for dec.More() {
			if err := scanJournalValue(dec, element); err != nil {
				return err
			}
		}
	default:
		return bad
	}
	if token, err = dec.Token(); err != nil || token != close {
		return bad
	}
	return nil
}
