package contract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func acpRequestFixtures(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/acp-v1/requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestACPBridgeRequestsStrictGolden(t *testing.T) {
	decoders := map[string]func([]byte) error{
		"bind":     func(raw []byte) error { _, err := DecodeACPBindRequest(raw); return err },
		"activate": func(raw []byte) error { _, err := DecodeACPActivateRequest(raw); return err },
		"claim":    func(raw []byte) error { _, err := DecodeACPClaimRequest(raw); return err },
		"begin":    func(raw []byte) error { _, err := DecodeACPBeginRequest(raw); return err },
		"receive":  func(raw []byte) error { _, err := DecodeACPReceiveRequest(raw); return err },
	}
	for name, raw := range acpRequestFixtures(t) {
		t.Run(name, func(t *testing.T) {
			decode := decoders[name]
			if err := decode(raw); err != nil {
				t.Fatalf("valid golden: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				delete(fields, key)
				missing, _ := json.Marshal(fields)
				if err := decode(missing); err == nil {
					t.Errorf("missing %s admitted", key)
				}
				fields[key] = json.RawMessage(`null`)
				null, _ := json.Marshal(fields)
				if err := decode(null); err == nil {
					t.Errorf("null %s admitted", key)
				}
				fields[key] = value
			}
			for label, bad := range map[string][]byte{
				"unknown":    append([]byte(`{"unexpected":true,`), raw[1:]...),
				"duplicate":  append([]byte(`{"schema_version":1,`), raw[1:]...),
				"case_alias": []byte(strings.Replace(string(raw), `"schema_version"`, `"Schema_version"`, 1)),
				"trailing":   append(append([]byte(nil), raw...), []byte(` {}`)...),
				"oversize":   append(append([]byte(nil), raw...), []byte(strings.Repeat(" ", 65537))...),
				"hook_token": append([]byte(`{"hook_capability":"secret",`), raw[1:]...),
			} {
				if err := decode(bad); err == nil {
					t.Errorf("%s admitted", label)
				}
			}
			for key, values := range map[string][]string{
				"owner_mode":            {`"native"`, `"managed"`},
				"owner_capability_file": {`"relative"`, `"/private/../owner"`},
				"owner_epoch":           {`0`, `9223372036854775808`},
				"native_session_id":     {`""`, `"\ud800"`, `"` + strings.Repeat("界", 171) + `"`},
				"turn_ordinal":          {`0`, `-1`, `9223372036854775808`},
				"rpc_request_id":        {`0`, `-1`, `9223372036854775808`},
				"stop_reason":           {`""`, `"` + strings.Repeat("x", 129) + `"`},
				"transport_mode":        {`"hook_pull"`},
				"provider_id":           {`"claude"`},
			} {
				original, exists := fields[key]
				if !exists {
					continue
				}
				for _, value := range values {
					fields[key] = json.RawMessage(value)
					bad, _ := json.Marshal(fields)
					if err := decode(bad); err == nil {
						t.Errorf("%s=%s admitted", key, value)
					}
				}
				fields[key] = original
			}
		})
	}
}

func TestACPBridgeCanonicalRequestKeys(t *testing.T) {
	f := acpRequestFixtures(t)
	a, err := DecodeACPActivateRequest(f["activate"])
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeACPBeginRequest(f["begin"])
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeACPReceiveRequest(f["receive"])
	if err != nil {
		t.Fatal(err)
	}
	if ACPActivateRequestID(a) != a.RequestID || ACPBeginRequestID(b) != b.RequestID || ACPReceiveRequestID(r) != r.RequestID {
		t.Fatal("canonical keys disagree with independent golden")
	}
	// Auth proofs can rotate paths without changing the mutation being retried.
	b.OwnerCapabilityFile = "/private/other-owner"
	b.ControlFile = "/private/other-control"
	b.ClaimToken = "another-proof"
	if ACPBeginRequestID(b) != b.RequestID {
		t.Fatal("auth proof changed mutation identity")
	}
	before := ACPReceiveRequestID(r)
	for name, mutate := range map[string]func(*ACPReceiveRequest){
		"binding":  func(v *ACPReceiveRequest) { v.BindingID += "x" },
		"epoch":    func(v *ACPReceiveRequest) { v.OwnerEpoch++ },
		"session":  func(v *ACPReceiveRequest) { v.NativeSessionID += "x" },
		"batch":    func(v *ACPReceiveRequest) { v.BatchID += "x" },
		"attempt":  func(v *ACPReceiveRequest) { v.AttemptID += "x" },
		"render":   func(v *ACPReceiveRequest) { v.RenderSHA256 = strings.Repeat("f", 64) },
		"prompt":   func(v *ACPReceiveRequest) { v.PromptSHA256 = strings.Repeat("f", 64) },
		"ordinal":  func(v *ACPReceiveRequest) { v.TurnOrdinal++ },
		"rpc":      func(v *ACPReceiveRequest) { v.RPCRequestID++ },
		"response": func(v *ACPReceiveRequest) { v.ResponseSHA256 = strings.Repeat("f", 64) },
		"stop":     func(v *ACPReceiveRequest) { v.StopReason = "max_tokens" },
	} {
		changed := r
		mutate(&changed)
		if ACPReceiveRequestID(changed) == before {
			t.Errorf("%s omitted from identity", name)
		}
		bad, _ := json.Marshal(changed)
		if _, err := DecodeACPReceiveRequest(bad); err == nil {
			t.Errorf("%s changed under old request ID", name)
		}
	}
	if ACPAttemptID("a", 1, "bc") == ACPAttemptID("ab", 1, "c") || ACPAttemptID("a", 1, "b") == ACPAttemptID("a", 2, "b") {
		t.Fatal("ambiguous attempt key")
	}
}
