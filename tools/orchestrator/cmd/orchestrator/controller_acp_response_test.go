package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestControllerACPResponsesRequireEveryFieldAndNeverMerge(t *testing.T) {
	_, claim := acpPublicClaimFixture(t)
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"binding", &contract.BridgeBindingView{SchemaVersion: 1, BindingID: "old-binding", Status: "ACTIVE", NativeSessionID: "original-session"}},
		{"claim", &claim},
		{"receive", &contract.BridgeReceiveReceipt{ReceiptID: "old-receipt", BatchID: "old-batch"}},
		{"begin", &controllerACPCommitReceipt{Status: "committed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if err = decodeControllerACPResponse(full, tc.value); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(full, &fields) != nil {
				t.Fatal("fixture")
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				original := fields[key]
				for _, mode := range []string{"missing", "null"} {
					t.Run(key+"/"+mode, func(t *testing.T) {
						if mode == "missing" {
							delete(fields, key)
						} else {
							fields[key] = json.RawMessage("null")
						}
						raw, _ := json.Marshal(fields)
						if err := decodeControllerACPResponse(raw, tc.value); err == nil {
							t.Fatal("sparse reply inherited prior fields")
						}
						after, _ := json.Marshal(tc.value)
						if !bytes.Equal(full, after) {
							t.Fatal("failed decode partly mutated prior response")
						}
						fields[key] = original
					})
				}
			}
		})
	}
}
