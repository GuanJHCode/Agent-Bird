package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func ownerWirePermit(t *testing.T) (OriginalOwnerBinding, OwnerControlPermit, ed25519.PrivateKey) {
	t.Helper()
	b, key := ownerContractBinding()
	b, err := SignOriginalOwnerBinding(b, key)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := OriginalOwnerScopeSHA256(b.Scope)
	identity, _ := OriginalOwnerIdentitySHA256(b.Owner)
	p, err := SignOwnerControlPermit(OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: b.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorKeyID: b.CoordinatorKeyID, CoordinatorEpoch: 3, ControlRevision: 1, NewHost: b.FirstHost, RequestID: "bind", RequestSHA256: strings.Repeat("b", 64), PermitID: "permit"}, key)
	if err != nil {
		t.Fatal(err)
	}
	return b, p, key
}

func TestOriginalOwnerCommandSignsFullUnionAndControl(t *testing.T) {
	_, permit, key := ownerWirePermit(t)
	body := json.RawMessage(`{"command_id":"launch-command","host_id":"producer","run_id":"run","task_id":"task","attempt_id":"attempt","segment_id":"segment","work_revision":1,"execution_epoch":3,"slot_token":"slot","launch_intent_hash":"digest","reservation_id":"reservation","budget_group_id":"budget","granted_active_ms":1000,"deadline_unix_ms":5000,"adapter_payload":{"kind":"fake","args":["/usr/bin/true"]},"report_capability_id":"report"}`)
	command, err := SignOwnerCommand(OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: "launch-command", Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyOwnerCommand(command, key.Public().(ed25519.PublicKey), permit); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	decoded, err := DecodeOwnerCommand(raw)
	if err != nil || string(decoded.Body) != string(body) {
		t.Fatal("signed full grant did not round trip", err)
	}
	for _, name := range []string{"body", "body_hash", "scope", "revision", "epoch", "new_host", "request", "kind", "signature", "unknown", "duplicate", "null", "oversize", "other_permit"} {
		t.Run(name, func(t *testing.T) {
			v := command
			expected := permit
			switch name {
			case "body":
				v.Body = bytes.Replace(v.Body, []byte("/usr/bin/true"), []byte("/usr/bin/false"), 1)
			case "body_hash":
				v.BodySHA256 = strings.Repeat("c", 64)
			case "scope":
				v.Permit.ScopeSHA256 = strings.Repeat("d", 64)
			case "revision":
				v.Permit.ControlRevision++
			case "epoch":
				v.Permit.CoordinatorEpoch++
			case "new_host":
				v.Permit.NewHost.PID++
			case "request":
				v.RequestID = "other"
			case "kind":
				v.Kind = "stop"
			case "signature":
				v.Signature = permit.Signature
			case "other_permit":
				expected.PermitID = "other"
			default:
				altered := append([]byte(nil), raw...)
				switch name {
				case "unknown":
					altered = append([]byte(`{"unknown":1,`), altered[1:]...)
				case "duplicate":
					altered = bytes.Replace(altered, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
				case "null":
					altered = bytes.Replace(altered, body, []byte("null"), 1)
				case "oversize":
					altered = append(altered, bytes.Repeat([]byte(" "), OriginalOwnerEnvelopeLimit)...)
				}
				if _, err := DecodeOwnerCommand(altered); err == nil {
					t.Fatal("invalid union decoded")
				}
				return
			}
			if err := VerifyOwnerCommand(v, key.Public().(ed25519.PublicKey), expected); err == nil {
				t.Fatal("altered signed command accepted")
			}
		})
	}
}

func TestOriginalOwnerRPCChallengeAndReplyBindExactRequest(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	challenge, err := SignOwnerChallenge(OwnerChallenge{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, CoordinatorKeyID: b.CoordinatorKeyID, Nonce: strings.Repeat("e", 64)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyOwnerChallenge(challenge, key.Public().(ed25519.PublicKey), b.Scope.OwnerID); err != nil {
		t.Fatal(err)
	}
	request, err := SignOwnerRPCRequest(OwnerRPCRequest{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: p.ScopeSHA256, OwnerIdentitySHA256: p.OwnerIdentitySHA256, CoordinatorEpoch: 3, ControlRevision: 1, RequestID: "event-1", Kind: "event", Nonce: challenge.Nonce, Body: json.RawMessage(`{"producer_id":"producer","event_id":"event-1"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyOwnerRPCRequest(request, key.Public().(ed25519.PublicKey), challenge); err != nil {
		t.Fatal(err)
	}
	reply, err := SignOwnerRPCReply(OwnerRPCReply{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, RequestID: request.RequestID, RequestSHA256: OwnerRPCRequestSHA256(request), Body: json.RawMessage(`{"status":"durable"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyOwnerRPCReply(reply, key.Public().(ed25519.PublicKey), request); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"body", "epoch", "revision", "nonce", "scope", "identity", "id", "kind", "signature"} {
		t.Run(name, func(t *testing.T) {
			altered := request
			switch name {
			case "body":
				altered.Body = json.RawMessage(`{"event_id":"other"}`)
			case "epoch":
				altered.CoordinatorEpoch++
			case "revision":
				altered.ControlRevision++
			case "nonce":
				altered.Nonce = strings.Repeat("f", 64)
			case "scope":
				altered.ScopeSHA256 = strings.Repeat("f", 64)
			case "identity":
				altered.OwnerIdentitySHA256 = strings.Repeat("f", 64)
			case "id":
				altered.RequestID = "other"
			case "kind":
				altered.Kind = "activate"
			case "signature":
				altered.Signature = reply.Signature
			}
			if VerifyOwnerRPCRequest(altered, key.Public().(ed25519.PublicKey), challenge) == nil {
				t.Fatal("altered request authorized")
			}
			if VerifyOwnerRPCReply(reply, key.Public().(ed25519.PublicKey), altered) == nil {
				t.Fatal("ACK accepted for different request")
			}
		})
	}
	stale := challenge
	stale.Nonce = strings.Repeat("f", 64)
	if VerifyOwnerRPCRequest(request, key.Public().(ed25519.PublicKey), stale) == nil {
		t.Fatal("request replayed on new connection challenge")
	}
}

func TestOriginalOwnerCommandCoversDependencyVectorAndCanonicalJSON(t *testing.T) {
	_, p, key := ownerWirePermit(t)
	inputs := make([]AcceptedInput, 0, 2)
	for _, id := range []string{"a", "z"} {
		inputs = append(inputs, AcceptedInput{Event: Event{Version: 1, RunID: "run", TaskID: id, AttemptID: id + "-attempt", SegmentID: id + "-segment", WorkRevision: 1, ProducerID: "dependency-producer", EventID: id + "-event", EventRevision: 1, ActionSlot: id + "-slot", CommandID: id + "-command", Sequence: 1, Kind: EventResult, PayloadHash: strings.Repeat("a", 64), Artifact: &ArtifactRef{ID: id, Path: "/private/tmp/" + id, Size: 1, SHA256: strings.Repeat("a", 64)}}, AdapterPayload: json.RawMessage(`{"kind":"fake","prompt":"<accepted>"}`), DecisionCommandID: id + "-accept"})
	}
	grant := LaunchCommand{CommandID: "join-command", HostID: "producer", RunID: "run", TaskID: "join", AttemptID: "join-attempt", SegmentID: "join-segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: 3, SlotToken: "slot", GrantedActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"dependency_input_version":1,"source_task":"a"}}`), Input: &inputs[0], DependencyInputs: &DependencyInputVector{Version: 1, Inputs: inputs}}
	grant.LaunchIntentHash, _ = CandidateIntentHash(grant)
	body, _ := json.MarshalIndent(grant, "", " ")
	command, err := SignOwnerCommand(OwnerCommand{Version: 1, Permit: p, Kind: "launch", RequestID: grant.CommandID, Body: body}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	decoded, err := DecodeOwnerCommand(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyOwnerCommand(decoded, key.Public().(ed25519.PublicKey), p); err != nil {
		t.Fatal("canonical HTML/whitespace drifted signed body", err)
	}
	var copied LaunchCommand
	if err = DecodeOwnerRuntimeStrict(decoded.Body, &copied); err != nil || copied.DependencyInputs == nil || len(copied.DependencyInputs.Inputs) != 2 {
		t.Fatal("vector truncated", err)
	}
	copied.DependencyInputs.Inputs[1].DecisionCommandID = "foreign-accept"
	copied.LaunchIntentHash, _ = CandidateIntentHash(copied)
	changed, _ := json.Marshal(copied)
	decoded.Body = changed
	decoded.BodySHA256 = ownerRawSHA(changed)
	if VerifyOwnerCommand(decoded, key.Public().(ed25519.PublicKey), p) == nil {
		t.Fatal("non-source dependency tamper survived signature")
	}
}

func TestOriginalOwnerWireRejectsUnknownUnionAndMalformedJSON(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	for _, body := range []string{`null`, `[]`, `{"x":1,"x":2}`, `{"nested":{"x":1,"x":2}}`} {
		if _, err := SignOwnerRPCRequest(OwnerRPCRequest{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: p.ScopeSHA256, OwnerIdentitySHA256: p.OwnerIdentitySHA256, CoordinatorEpoch: 3, ControlRevision: 1, RequestID: "request", Kind: "event", Nonce: strings.Repeat("e", 64), Body: json.RawMessage(body)}, key); err == nil {
			t.Fatal("malformed signed body accepted", body)
		}
	}
	for _, kind := range []string{"stop", "terminal"} {
		body := json.RawMessage(`{"command_id":"stop-command","segment_id":"segment","reason":"owner_stop_requested","deadline_unix_ms":1000}`)
		id := "stop-command"
		if kind == "terminal" {
			body = json.RawMessage(`{"reason":"coordinator_exit"}`)
			id = "terminal"
		}
		c, err := SignOwnerCommand(OwnerCommand{Version: 1, Permit: p, Kind: kind, RequestID: id, Body: body}, key)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(c)
		d, err := DecodeOwnerCommand(wire)
		if err != nil || VerifyOwnerCommand(d, key.Public().(ed25519.PublicKey), p) != nil {
			t.Fatal(kind, err)
		}
		c.Kind = "future"
		if _, err = SignOwnerCommand(c, key); err == nil {
			t.Fatal("unknown command union accepted")
		}
	}
}

func TestOriginalOwnerRPCDecodersPreserveRawBodiesAndRejectAliases(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	request, err := SignOwnerRPCRequest(OwnerRPCRequest{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: p.ScopeSHA256, OwnerIdentitySHA256: p.OwnerIdentitySHA256, CoordinatorEpoch: 3, ControlRevision: 1, RequestID: "event", Kind: "event", Nonce: strings.Repeat("e", 64), Body: json.RawMessage("{ \"payload\": \"<example>\" }")}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	decoded, err := DecodeOwnerRPCRequest(raw)
	if err != nil || OwnerRPCRequestSHA256(decoded) != OwnerRPCRequestSHA256(request) {
		t.Fatal("request raw body changed", err)
	}
	for _, name := range []string{"alias", "duplicate", "missing", "null"} {
		t.Run(name, func(t *testing.T) {
			altered := raw
			switch name {
			case "alias":
				altered = bytes.Replace(raw, []byte(`"owner_id"`), []byte(`"Owner_ID"`), 1)
			case "duplicate":
				altered = bytes.Replace(raw, []byte(`"request_id":"event"`), []byte(`"request_id":"event","request_id":"event"`), 1)
			case "missing":
				altered = bytes.Replace(raw, []byte(`"control_revision":1,`), nil, 1)
			case "null":
				altered = bytes.Replace(raw, []byte(`"control_revision":1`), []byte(`"control_revision":null`), 1)
			}
			if _, err := DecodeOwnerRPCRequest(altered); err == nil {
				t.Fatal("ambiguous RPC decoded")
			}
		})
	}
	reply, err := SignOwnerRPCReply(OwnerRPCReply{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, RequestID: request.RequestID, RequestSHA256: OwnerRPCRequestSHA256(request), Body: json.RawMessage(`{"status":"durable"}`)}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(reply)
	copy, err := DecodeOwnerRPCReply(raw)
	if err != nil || VerifyOwnerRPCReply(copy, key.Public().(ed25519.PublicKey), request) != nil {
		t.Fatal("reply lost request binding", err)
	}
}

func TestOriginalOwnerWireDecodersRequireSignature(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	command, _ := SignOwnerCommand(OwnerCommand{Version: 1, Permit: p, Kind: "terminal", RequestID: "terminal", Body: json.RawMessage(`{"reason":"host_retired"}`)}, key)
	challenge, _ := SignOwnerChallenge(OwnerChallenge{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, CoordinatorKeyID: b.CoordinatorKeyID, Nonce: strings.Repeat("e", 64)}, key)
	request, _ := SignOwnerRPCRequest(OwnerRPCRequest{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: p.ScopeSHA256, OwnerIdentitySHA256: p.OwnerIdentitySHA256, CoordinatorEpoch: 3, ControlRevision: 1, RequestID: "event", Kind: "event", Nonce: challenge.Nonce, Body: json.RawMessage(`{}`)}, key)
	reply, _ := SignOwnerRPCReply(OwnerRPCReply{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, RequestID: request.RequestID, RequestSHA256: OwnerRPCRequestSHA256(request), Body: json.RawMessage(`{}`)}, key)
	for _, tc := range []struct {
		name   string
		value  any
		decode func([]byte) error
	}{
		{"command", command, func(raw []byte) error { _, e := DecodeOwnerCommand(raw); return e }},
		{"challenge", challenge, func(raw []byte) error { _, e := DecodeOwnerChallenge(raw); return e }},
		{"request", request, func(raw []byte) error { _, e := DecodeOwnerRPCRequest(raw); return e }},
		{"reply", reply, func(raw []byte) error { _, e := DecodeOwnerRPCReply(raw); return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.value)
			if err := tc.decode(raw); err != nil {
				t.Fatal("valid signed fixture", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["signature"] = json.RawMessage(`""`)
			unsigned, _ := json.Marshal(fields)
			if err := tc.decode(unsigned); err == nil {
				t.Fatal("unsigned frame accepted by decoder")
			}
		})
	}
}

func TestOriginalOwnerRPCReplySigningRejectsAmbiguousBody(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	_, err := SignOwnerRPCReply(OwnerRPCReply{Version: 1, OwnerID: b.Scope.OwnerID, CoordinatorEpoch: 3, RequestID: "reply", RequestSHA256: p.RequestSHA256, Body: json.RawMessage(`{"status":"accepted","status":"rejected"}`)}, key)
	if err == nil {
		t.Fatal("signed reply admits ambiguous duplicate body")
	}
}
