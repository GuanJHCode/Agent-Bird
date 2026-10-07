package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func ownerBootstrapFixture() (OwnerRuntimeBootstrap, OwnerTransportBootstrap) {
	binding, key := ownerContractBinding()
	binding.Scope.ProducerID = binding.Scope.LaunchID
	token := strings.Repeat("c", 64)
	tokenHash, _ := OriginalOwnerBootstrapSHA256(token)
	intent := OriginalOwnerIntent{Scope: binding.Scope, BootstrapID: "bootstrap", BootstrapTokenSHA256: tokenHash, CoordinatorKeyID: binding.CoordinatorKeyID, RequestID: "prepare", RequestSHA256: strings.Repeat("d", 64), StartupDeadlineUnixMS: 10000, FirstHost: binding.FirstHost, CoordinatorEpoch: 7}
	state := "/private/owner-state"
	publicKey := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	runtime := OwnerRuntimeBootstrap{Version: 1, StateDir: state, SpoolRoot: filepath.Join(state, "host-spool", intent.Scope.LaunchID), BootstrapToken: token, Prepared: OwnerPrepared{Intent: intent, CoordinatorPublicKey: publicKey, RPCSocketPath: filepath.Join(state, "owner.sock")}}
	transport := OwnerTransportBootstrap{Version: 1, StateDir: state, Intent: intent, Owner: binding.Owner, CoordinatorPublicKey: publicKey}
	return runtime, transport
}

func TestOwnerRuntimeBootstrapBindsEnrollmentAndPrivateLayout(t *testing.T) {
	valid, _ := ownerBootstrapFixture()
	raw, _ := json.Marshal(valid)
	if _, err := DecodeOwnerRuntimeBootstrap(raw); err != nil {
		t.Fatal("valid enrollment bootstrap rejected", err)
	}
	mutations := map[string]func(*OwnerRuntimeBootstrap){
		"version":        func(b *OwnerRuntimeBootstrap) { b.Version++ },
		"relative_state": func(b *OwnerRuntimeBootstrap) { b.StateDir = "relative" },
		"root_state": func(b *OwnerRuntimeBootstrap) {
			b.StateDir = "/"
			b.SpoolRoot = "/host-spool/launch"
			b.Prepared.RPCSocketPath = "/owner.sock"
		},
		"unclean_state": func(b *OwnerRuntimeBootstrap) { b.StateDir += "/../owner-state" },
		"spool_escape":  func(b *OwnerRuntimeBootstrap) { b.SpoolRoot = "/private/other" },
		"rpc_escape":    func(b *OwnerRuntimeBootstrap) { b.Prepared.RPCSocketPath = "/private/other.sock" },
		"launch_traversal": func(b *OwnerRuntimeBootstrap) {
			b.Prepared.Intent.Scope.LaunchID = "../other"
			b.Prepared.Intent.Scope.ProducerID = "../other"
			b.SpoolRoot = filepath.Join(b.StateDir, "host-spool", "../other")
		},
		"launch_backslash": func(b *OwnerRuntimeBootstrap) {
			b.Prepared.Intent.Scope.LaunchID = `x\other`
			b.Prepared.Intent.Scope.ProducerID = `x\other`
			b.SpoolRoot = filepath.Join(b.StateDir, "host-spool", `x\other`)
		},
		"producer":       func(b *OwnerRuntimeBootstrap) { b.Prepared.Intent.Scope.ProducerID = "other" },
		"scope":          func(b *OwnerRuntimeBootstrap) { b.Prepared.Intent.Scope.ExecutableSHA256 = "invalid" },
		"token":          func(b *OwnerRuntimeBootstrap) { b.BootstrapToken = strings.Repeat("a", 64) },
		"token_encoding": func(b *OwnerRuntimeBootstrap) { b.BootstrapToken = strings.ToUpper(b.BootstrapToken) },
		"key":            func(b *OwnerRuntimeBootstrap) { b.Prepared.CoordinatorPublicKey = strings.Repeat("a", 64) },
		"key_encoding": func(b *OwnerRuntimeBootstrap) {
			b.Prepared.CoordinatorPublicKey = strings.ToUpper(b.Prepared.CoordinatorPublicKey)
		},
		"first_host": func(b *OwnerRuntimeBootstrap) { b.Prepared.Intent.FirstHost.PID = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := valid
			mutate(&b)
			raw, _ := json.Marshal(b)
			if _, err := DecodeOwnerRuntimeBootstrap(raw); err == nil {
				t.Fatal("invalid enrollment bootstrap accepted")
			}
		})
	}
}

func TestOwnerTransportBootstrapIsPublicAndStrict(t *testing.T) {
	runtime, transport := ownerBootstrapFixture()
	runtimeRaw, _ := json.Marshal(runtime)
	transportRaw, _ := json.Marshal(transport)
	if _, err := DecodeOwnerTransportBootstrap(transportRaw); err != nil {
		t.Fatal("valid public transport bootstrap rejected", err)
	}
	for name, mutate := range map[string]func(*OwnerTransportBootstrap){
		"version": func(b *OwnerTransportBootstrap) { b.Version++ },
		"state":   func(b *OwnerTransportBootstrap) { b.StateDir = "/" },
		"owner":   func(b *OwnerTransportBootstrap) { b.Owner.PublicKey = "invalid" },
		"key":     func(b *OwnerTransportBootstrap) { b.CoordinatorPublicKey = strings.Repeat("a", 64) },
		"intent":  func(b *OwnerTransportBootstrap) { b.Intent.CoordinatorEpoch = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			b := transport
			mutate(&b)
			raw, _ := json.Marshal(b)
			if _, err := DecodeOwnerTransportBootstrap(raw); err == nil {
				t.Fatal("invalid transport bootstrap accepted")
			}
		})
	}
	for _, name := range []string{"launch_token", "control_token", "private_key", "duplicate", "missing", "trailing", "oversize"} {
		t.Run(name, func(t *testing.T) {
			for _, transportMode := range []bool{false, true} {
				raw := runtimeRaw
				if transportMode {
					raw = transportRaw
				}
				switch name {
				case "duplicate":
					raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
				case "missing":
					raw = bytes.Replace(raw, []byte(`"version":1,`), nil, 1)
				case "trailing":
					raw = append(append([]byte{}, raw...), []byte(`{}`)...)
				case "oversize":
					raw = append(bytes.Repeat([]byte(" "), OriginalOwnerEnvelopeLimit), raw...)
				default:
					raw = append([]byte(`{"`+name+`":"forbidden",`), raw[1:]...)
				}
				if transportMode {
					if _, err := DecodeOwnerTransportBootstrap(raw); err == nil {
						t.Fatal("transport accepted ambiguous or credential-bearing bootstrap")
					}
				} else if _, err := DecodeOwnerRuntimeBootstrap(raw); err == nil {
					t.Fatal("owner accepted ambiguous or credential-bearing bootstrap")
				}
			}
		})
	}
	for _, raw := range [][]byte{runtimeRaw, append([]byte(`{"bootstrap_token":"forbidden",`), transportRaw[1:]...)} {
		if _, err := DecodeOwnerTransportBootstrap(raw); err == nil {
			t.Fatal("replacement transport received enrollment material")
		}
	}
}
