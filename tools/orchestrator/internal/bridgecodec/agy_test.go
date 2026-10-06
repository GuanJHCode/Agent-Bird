package bridgecodec

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAGYPreInvocationWire(t *testing.T) {
	b := fixtureBatch()
	wire, err := WireBytes(b, b.AttemptID, "agy", "PreInvocation")
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := json.Marshal(b)
	want, _ := json.Marshal(map[string]any{"injectSteps": []any{map[string]string{"ephemeralMessage": additionalContextPrefix + string(inner)}}})
	if !bytes.Equal(wire, want) {
		t.Fatalf("wire=%s want=%s", wire, want)
	}
	for _, pair := range [][2]string{{"claude", "PreInvocation"}, {"agy", "SessionStart"}, {"agy", "Stop"}, {"agy", "PostInvocation"}} {
		if _, err := WireBytes(b, b.AttemptID, pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestAGYGoldenNoMessageAndFinalBoundary(t *testing.T) {
	b := fixtureBatch()
	wire, err := WireBytes(b, b.AttemptID, "agy", "PreInvocation")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(fixturePath(t, "agy-pre-invocation.json"))
	if err != nil || !bytes.Equal(wire, golden) {
		t.Fatalf("AGY golden changed err=%v", err)
	}
	noop, err := NoMessageBytes("agy", "PreInvocation")
	if err != nil || string(noop) != `{"injectSteps":[]}` {
		t.Fatalf("no-message=%s err=%v", noop, err)
	}
	for _, pair := range [][2]string{{"claude", "PreInvocation"}, {"agy", "Stop"}, {"agy", "UserPromptSubmit"}} {
		if _, err := NoMessageBytes(pair[0], pair[1]); err == nil {
			t.Fatalf("no-message unsupported %v", pair)
		}
	}
	// The Claude exact 8192 fixture's wrapper costs 37 more bytes than AGY.
	// Add those 37 UTF-8 content bytes without exceeding any summary bound.
	b = fixtureBatch()
	b.Events = nil
	b.DeliveryReceipts[0].EventIDs = nil
	for i := 1; i <= 8; i++ {
		e := fixtureBatch().Events[0]
		e.EventID = "event-" + string(rune('0'+i))
		e.Summary = strings.Repeat("\"", 133)
		if i == 1 {
			e.Summary = "界\x01\n" + e.Summary + strings.Repeat("a", 37)
		}
		if i == 8 {
			e.Summary += strings.Repeat("a", 356)
		}
		b.Events = append(b.Events, e)
		b.DeliveryReceipts[0].EventIDs = append(b.DeliveryReceipts[0].EventIDs, e.EventID)
	}
	b.RenderSHA256, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	wire, err = WireBytes(b, b.AttemptID, "agy", "PreInvocation")
	if err != nil || len(wire) != 8192 || !utf8.Valid(wire) {
		t.Fatalf("exact AGY boundary bytes=%d err=%v", len(wire), err)
	}
	b.Events[7].Summary += "a"
	b.RenderSHA256, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WireBytes(b, b.AttemptID, "agy", "PreInvocation"); err == nil {
		t.Fatal("8193 bytes accepted")
	}
}
