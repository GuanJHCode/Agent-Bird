package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidationCommandPinScope(t *testing.T) {
	for _, kind := range []string{"legacy", "valid", "uppercase", "short", "review"} {
		t.Run(kind, func(t *testing.T) {
			action := map[string]any{"version": 1, "operation": "validate", "source_task": "edit", "command": []string{"/usr/bin/true"}}
			if kind != "legacy" {
				action["command_sha256"] = strings.Repeat("a", 64)
			}
			switch kind {
			case "uppercase":
				action["command_sha256"] = strings.Repeat("A", 64)
			case "short":
				action["command_sha256"] = "abcd"
			case "review":
				action["operation"] = "review"
			}
			raw, _ := json.Marshal(map[string]any{"kind": "candidate", "candidate_action": action})
			_, err := DecodeInvocationPayload(raw)
			if (err == nil) != (kind == "legacy" || kind == "valid") {
				t.Fatalf("decode %s: %v", kind, err)
			}
		})
	}
}
