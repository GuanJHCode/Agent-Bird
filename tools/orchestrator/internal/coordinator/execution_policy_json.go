package coordinator

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// Preserve adapter bytes while closing the task's public keys and duplicate
// task keys. Policy's own decoder closes its nested keys independently.
func (t *TaskRequest) UnmarshalJSON(raw []byte) error {
	type plain TaskRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid_task_request")
	}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(plain{})
	for i := 0; i < typ.NumField(); i++ {
		allowed[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !allowed[key] {
			return errors.New("invalid_task_request")
		}
		seen[key] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return errors.New("invalid_task_request")
		}
		if key == "diagnostics" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("invalid_task_request")
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	if _, err = dec.Token(); err != io.EOF {
		return errors.New("invalid_task_request")
	}
	var value plain
	if json.Unmarshal(raw, &value) != nil {
		return errors.New("invalid_task_request")
	}
	*t = TaskRequest(value)
	return nil
}
