package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostArtifactReceiptStrictBoundedWire(t *testing.T) {
	valid := `{"version":1,"descriptor":{"version":1},"proof":{"intake_id":"slot"}}`
	for name, raw := range map[string]string{
		"null": `null`, "version": strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"alias":            strings.Replace(valid, `"proof"`, `"PROOF"`, 1),
		"unknown":          strings.Replace(valid, `"version":1,`, `"version":1,"limit":999,`, 1),
		"duplicate":        strings.Replace(valid, `"version":1,`, `"version":1,"version":1,`, 1),
		"nested_duplicate": strings.Replace(valid, `"intake_id":"slot"`, `"intake_id":"slot","intake_id":"other"`, 1),
		"missing":          `{"version":1,"descriptor":{}}`,
		"null_descriptor":  `{"version":1,"descriptor":null,"proof":{}}`,
		"array_proof":      `{"version":1,"descriptor":{},"proof":[]}`,
		"large_descriptor": `{"version":1,"descriptor":{"pad":"` + strings.Repeat("x", 1024) + `"},"proof":{}}`,
		"large_proof":      `{"version":1,"descriptor":{},"proof":{"pad":"` + strings.Repeat("x", 4096) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var r HostArtifactReceipt
			if err := json.Unmarshal([]byte(raw), &r); err == nil {
				t.Fatal("invalid Host receipt decoded", name)
			}
		})
	}
	var r HostArtifactReceipt
	if err := json.Unmarshal([]byte(valid), &r); err != nil {
		t.Fatal(err)
	}
	back, err := json.Marshal(r)
	if err != nil || string(back) != valid {
		t.Fatal("receipt changed bytes", string(back), err)
	}
}

func TestArtifactRefLegacyWireRemainsUnchanged(t *testing.T) {
	raw, err := json.Marshal(ArtifactRef{ID: "a", Path: "/private/a", Size: 1, SHA256: "hash"})
	if err != nil || string(raw) != `{"id":"a","path":"/private/a","size":1,"sha256":"hash"}` {
		t.Fatal(string(raw), err)
	}
}

func TestHostArtifactReceiptTotalBoundMatchesDurableRef(t *testing.T) {
	// Each component is under its own cap; the complete durable receipt is not.
	descriptor := json.RawMessage(`{"pad":"` + strings.Repeat("x", 900) + `"}`)
	proof := json.RawMessage(`{"pad":"` + strings.Repeat("y", 3300) + `"}`)
	r := HostArtifactReceipt{Version: 1, Descriptor: descriptor, Proof: proof}
	if err := r.Validate(); err == nil {
		t.Fatal("receipt exceeded durable 4096-byte ref bound")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostArtifactReceipt
	if err = json.Unmarshal(raw, &decoded); err == nil {
		t.Fatal("oversize durable receipt decoded")
	}
	// The cap includes the envelope, not just the descriptor and proof fields.
	base := HostArtifactReceipt{Version: 1, Descriptor: json.RawMessage(`{}`), Proof: json.RawMessage(`{"pad":""}`)}
	body, _ := json.Marshal(base)
	base.Proof = json.RawMessage(`{"pad":"` + strings.Repeat("z", 4096-len(body)) + `"}`)
	exact, _ := json.Marshal(base)
	if len(exact) != 4096 || base.Validate() != nil {
		t.Fatal("exact receipt bound rejected", len(exact))
	}
}
