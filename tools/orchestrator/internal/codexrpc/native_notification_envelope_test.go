package codexrpc

import (
	"errors"
	"io"
	"testing"
)

func TestPinnedTimestampedNotificationAfterMatchedInitialize(t *testing.T) {
	f := newEarlyReplyFixture(t, true)
	done := initializeShapeCall(f)
	job := initializeShapeWritingJob(t, f)
	f.replyAndObserve(t, initializeShapeReply)
	f.requireHeld(t, job)
	t.Log("original reader has accepted matched initialize; original Call still owns actual Write/cancellation")
	// Exact observed keys/types with synthetic values, from fixed 0.154
	// ServerNotificationEnvelope and RemoteControlStatusChangedNotification.
	wire := `{"method":"remoteControl/status/changed","params":{"status":"disabled","installationId":"synthetic-installation","serverName":"synthetic-server","environmentId":null},"emittedAtMs":1}` + "\n"
	if _, err := io.WriteString(f.peer, wire); err != nil {
		t.Fatal("real timestamped notification Write failed")
	}
	event := eventBefore(t, f.c, f.ctx)
	if event.Notification == nil || event.Notification.Method != "remoteControl/status/changed" || event.Request != nil {
		t.Fatal("pinned timestamp metadata rejected or gained request authority")
	}
	f.requireHeld(t, job)
	f.stream.releaseReturn()
	if value := f.result(t, done); value.err != nil {
		t.Fatal("native notification broke the original matched initialize Call")
	}
}

func TestPinnedNotificationTimestampIsOptionalExactInt64(t *testing.T) {
	for _, timestamp := range []string{"", "null", "-9223372036854775808", "-1", "0", "9007199254740993", "9223372036854775807"} {
		name := timestamp
		if name == "" {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			field := ""
			if timestamp != "" {
				field = `,"emittedAtMs":` + timestamp
			}
			f, err := decodeFrame([]byte(`{"method":"warning","params":{"message":"synthetic"}` + field + `}`))
			if err != nil || f.kind != notificationFrame || f.method != "warning" || f.id.valid {
				t.Fatalf("pinned optional i64 notification rejected: %v", err)
			}
		})
	}
}

func TestPinnedTimestampRejectsOtherTypesFramesAndUnknownFields(t *testing.T) {
	cases := map[string]string{
		"decimal":            `{"method":"warning","emittedAtMs":1.0}`,
		"exponent":           `{"method":"warning","emittedAtMs":1e0}`,
		"fraction":           `{"method":"warning","emittedAtMs":1.5}`,
		"overflow":           `{"method":"warning","emittedAtMs":9223372036854775808}`,
		"underflow":          `{"method":"warning","emittedAtMs":-9223372036854775809}`,
		"string":             `{"method":"warning","emittedAtMs":"1"}`,
		"bool":               `{"method":"warning","emittedAtMs":true}`,
		"array":              `{"method":"warning","emittedAtMs":[]}`,
		"object":             `{"method":"warning","emittedAtMs":{}}`,
		"request":            `{"id":"remote","method":"approval","emittedAtMs":1}`,
		"request_null":       `{"id":1,"method":"approval","emittedAtMs":null}`,
		"result":             `{"id":"local-1","result":null,"emittedAtMs":1}`,
		"result_null":        `{"id":"local-1","result":{},"emittedAtMs":null}`,
		"error":              `{"id":"local-1","error":{"code":1,"message":"synthetic"},"emittedAtMs":1}`,
		"error_null":         `{"id":"local-1","error":{"code":1,"message":"synthetic"},"emittedAtMs":null}`,
		"duplicate":          `{"method":"warning","emittedAtMs":1,"emittedAtMs":2}`,
		"escaped_duplicate":  `{"method":"warning","emittedAtMs":1,"emittedAt\u004ds":2}`,
		"unknown_extra":      `{"method":"warning","emittedAtMs":1,"other":1}`,
		"notification_trace": `{"method":"warning","emittedAtMs":1,"trace":null}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeFrame([]byte(raw)); !errors.Is(err, ErrProtocol) {
				t.Fatal("timestamp widened a closed envelope boundary")
			}
		})
	}
}
