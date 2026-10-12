package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPinnedEnvelopePresenceAndExactIDs(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		kind      frameKind
	}{
		{"null_result", `{"id":"local-1","result":null}`, resultFrame},
		{"notification", `{"method":"turn/completed","params":null}`, notificationFrame},
		{"notification_optional_params", `{"method":"initialized"}`, notificationFrame},
		{"request_empty_string_id", `{"id":"","method":"item/tool/requestUserInput"}`, requestFrame},
		{"integer_above_float_precision", `{"id":9007199254740993,"method":"approval"}`, requestFrame},
		{"integer_min", `{"id":-9223372036854775808,"method":"approval"}`, requestFrame},
		{"integer_max", `{"id":9223372036854775807,"method":"approval"}`, requestFrame},
		{"trace_null", `{"id":"r","method":"approval","trace":null}`, requestFrame},
		{"trace_pinned", `{"id":"r","method":"approval","trace":{"traceparent":null,"tracestate":"fixture"}}`, requestFrame},
		{"paired_unicode_id", `{"id":"\uD83D\uDE00","method":"approval"}`, requestFrame},
		{"escaped_slash_id", `{"id":"a\/b","method":"approval"}`, requestFrame},
		{"remote_error", `{"id":"local-1","error":{"code":-32601,"message":"fixture","data":null}}`, errorFrame},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, err := decodeFrame([]byte(test.raw))
			if err != nil || f.kind != test.kind {
				t.Fatalf("pinned envelope not retained: %v", err)
			}
			if test.name == "integer_above_float_precision" {
				if value, ok := f.id.Int64Value(); !ok || value != 9007199254740993 {
					t.Fatal("int64 rounded through float64")
				}
			}
			if test.name == "null_result" && !bytes.Equal(f.result, []byte("null")) {
				t.Fatal("required null result lost")
			}
		})
	}
}

func TestAmbiguousMalformedEnvelopesAreRejected(t *testing.T) {
	for name, raw := range map[string]string{
		"array": `[]`, "null": `null`, "empty": `{}`,
		"trailing": `{"method":"n"} {}`, "truncated": `{"method":"n"`,
		"missing_result": `{"id":"r"}`, "mixed_success_error": `{"id":"r","result":null,"error":{"code":1,"message":"x"}}`,
		"mixed_request_response": `{"id":"r","method":"n","result":1}`,
		"response_params":        `{"id":"r","result":null,"params":null}`,
		"response_trace":         `{"id":"r","result":null,"trace":null}`,
		"unknown_key":            `{"method":"n","other":1}`, "notification_trace": `{"method":"n","trace":null}`,
		"null_id": `{"id":null,"method":"n"}`, "bool_id": `{"id":true,"method":"n"}`,
		"fractional_id": `{"id":1.5,"method":"n"}`, "decimal_id": `{"id":1.0,"method":"n"}`,
		"exponent_id": `{"id":1e0,"method":"n"}`, "id_overflow": `{"id":9223372036854775808,"method":"n"}`,
		"id_underflow": `{"id":-9223372036854775809,"method":"n"}`, "array_id": `{"id":[],"method":"n"}`,
		"duplicate_id":         `{"id":"a","id":"b","method":"n"}`,
		"duplicate_escaped_id": `{"id":"a","\u0069d":"b","method":"n"}`,
		"duplicate_result":     `{"id":"r","result":1,"result":2}`,
		"duplicate_method":     `{"method":"a","method":"b"}`,
		"null_method":          `{"method":null}`, "empty_method": `{"method":""}`, "numeric_method": `{"method":1}`,
		"error_null": `{"id":"r","error":null}`, "error_missing_code": `{"id":"r","error":{"message":"x"}}`,
		"error_missing_message": `{"id":"r","error":{"code":1}}`,
		"error_message_null":    `{"id":"r","error":{"code":1,"message":null}}`,
		"error_fraction":        `{"id":"r","error":{"code":1.5,"message":"x"}}`,
		"error_code_overflow":   `{"id":"r","error":{"code":9223372036854775808,"message":"x"}}`,
		"error_duplicate":       `{"id":"r","error":{"code":1,"code":2,"message":"x"}}`,
		"trace_type":            `{"id":"r","method":"n","trace":1}`,
		"trace_field_type":      `{"id":"r","method":"n","trace":{"traceparent":1}}`,
		"trace_duplicate":       `{"id":"r","method":"n","trace":{"traceparent":"a","traceparent":"b"}}`,
		"lone_surrogate":        `{"id":"\uD800","method":"n"}`,
		"lone_low_surrogate":    `{"id":"\uDC00","method":"n"}`,
		"utf8":                  string([]byte{'{', '"', 'm', 'e', 't', 'h', 'o', 'd', '"', ':', '"', 255, '"', '}'}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeFrame([]byte(raw)); !errors.Is(err, ErrProtocol) {
				t.Fatalf("invalid envelope accepted: %v", err)
			}
		})
	}
}

func TestScalarLimitsMeasureDecodedUTF8Bytes(t *testing.T) {
	for _, test := range []struct {
		name, value string
		id          bool
		want        error
	}{
		{"id_ascii_exact", strings.Repeat("x", 256), true, nil},
		{"id_ascii_over", strings.Repeat("x", 257), true, ErrIDLimit},
		{"id_utf8_exact", strings.Repeat("界", 85) + "x", true, nil},
		{"id_utf8_over", strings.Repeat("界", 86), true, ErrIDLimit},
		{"method_exact", strings.Repeat("x", 256), false, nil},
		{"method_over", strings.Repeat("x", 257), false, ErrMethodLimit},
		{"method_utf8_exact", strings.Repeat("界", 85) + "x", false, nil},
		{"method_utf8_over", strings.Repeat("界", 86), false, ErrMethodLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, _ := json.Marshal(test.value)
			var raw []byte
			if test.id {
				raw = append(append([]byte(`{"id":`), value...), []byte(`,"method":"n"}`)...)
			} else {
				raw = append(append([]byte(`{"method":`), value...), '}')
			}
			_, err := decodeFrame(raw)
			if !errors.Is(err, test.want) {
				t.Fatalf("scalar boundary wrong: %v", err)
			}
		})
	}
}

func TestLimitsCannotRaisePinnedMaxima(t *testing.T) {
	for _, limits := range []Limits{
		{FrameBytes: MaxFrameBytes + 1}, {LocalCalls: MaxLocalCalls + 1}, {RemoteRequests: MaxRemoteRequests + 1},
		{Events: MaxEvents + 1}, {ConsumedRequests: MaxConsumedRequests + 1}, {FrameBytes: -1},
	} {
		if _, err := limits.normalized(); !errors.Is(err, ErrLimits) {
			t.Fatal("hard maximum raised")
		}
	}
	limits, err := (Limits{}).normalized()
	if err != nil || limits != (Limits{MaxFrameBytes, MaxLocalCalls, MaxRemoteRequests, MaxEvents, MaxConsumedRequests}) {
		t.Fatal("default limits drifted")
	}
}
