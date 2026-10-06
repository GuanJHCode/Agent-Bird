package codexsession

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fixtureRemoteControlDisabled() map[string]any {
	return map[string]any{"status": "disabled", "installationId": "synthetic-installation", "serverName": "synthetic-server", "environmentId": nil}
}

func TestRunDisabledStatusKeepsExactSchemaAndBoundedDiscard(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want error
	}{
		{"environment_absent", func(m map[string]any) { delete(m, "environmentId") }, ErrCallback},
		{"empty_required_strings", func(m map[string]any) { m["installationId"] = ""; m["serverName"] = "" }, ErrCallback},
		{"exact_utf8_bound", func(m map[string]any) { m["installationId"] = strings.Repeat("界", 85) + "x" }, ErrCallback},
		{"connecting", func(m map[string]any) { m["status"] = "connecting" }, ErrPolicy},
		{"connected", func(m map[string]any) { m["status"] = "connected" }, ErrPolicy},
		{"errored", func(m map[string]any) { m["status"] = "errored" }, ErrPolicy},
		{"environment_nonnull", func(m map[string]any) { m["environmentId"] = "synthetic" }, ErrPolicy},
		{"environment_empty_string", func(m map[string]any) { m["environmentId"] = "" }, ErrPolicy},
		{"unknown_status", func(m map[string]any) { m["status"] = "future" }, ErrProtocol},
		{"null_status", func(m map[string]any) { m["status"] = nil }, ErrProtocol},
		{"extra_field", func(m map[string]any) { m["permission"] = true }, ErrProtocol},
		{"environment_wrong_type", func(m map[string]any) { m["environmentId"] = true }, ErrProtocol},
		{"installation_missing", func(m map[string]any) { delete(m, "installationId") }, ErrProtocol},
		{"server_missing", func(m map[string]any) { delete(m, "serverName") }, ErrProtocol},
		{"status_missing", func(m map[string]any) { delete(m, "status") }, ErrProtocol},
		{"installation_wrong_type", func(m map[string]any) { m["installationId"] = 1 }, ErrProtocol},
		{"server_null", func(m map[string]any) { m["serverName"] = nil }, ErrProtocol},
		{"installation_over_bound", func(m map[string]any) { m["installationId"] = strings.Repeat("界", 86) }, ErrStateLimit},
		{"server_over_bound", func(m map[string]any) { m["serverName"] = strings.Repeat("x", 257) }, ErrStateLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := fixtureRemoteControlDisabled()
			tc.edit(params)
			result := runScenario(t, fixtureScenario{before: func(method string, peer *fixturePeer) error {
				if method == "initialized" {
					return peer.notify("remoteControl/status/changed", params)
				}
				return nil
			}, callback: func(context.Context, ThreadBinding) error { return ErrCallback }})
			requireFailure(t, result, tc.want)
			wantCallbacks := 0
			if tc.want == ErrCallback {
				wantCallbacks = 1
			}
			if result.callbacks != wantCallbacks || result.out.TurnID != "" {
				t.Fatal("status changed original binding-only callback/zero-turn barrier")
			}
		})
	}
}

func TestRunLateAndQueuedActiveRemoteStatusCannotComplete(t *testing.T) {
	for _, phase := range []string{"binding", "pending_turn_after_terminal", "running"} {
		t.Run(phase, func(t *testing.T) {
			active := fixtureRemoteControlDisabled()
			active["status"] = "connected"
			result := runScenario(t, fixtureScenario{before: func(method string, peer *fixturePeer) error {
				if phase == "binding" && method == "thread/start" {
					return peer.notify("remoteControl/status/changed", active)
				}
				if phase == "pending_turn_after_terminal" && method == "turn/start" {
					if err := peer.terminal(fixtureTerminal()); err != nil {
						return err
					}
					return peer.notify("remoteControl/status/changed", active)
				}
				return nil
			}, after: func(peer *fixturePeer) error {
				if err := peer.notify("remoteControl/status/changed", active); err != nil {
					return err
				}
				return peer.terminal(fixtureTerminal())
			}})
			requireFailure(t, result, ErrPolicy)
			if phase == "pending_turn_after_terminal" && result.out.Status != "completed" {
				t.Fatal("fixture did not observe provisional terminal before policy rejection")
			}
		})
	}
}

func TestRunLaterDisabledStatusDoesNotChangeCompletedOutput(t *testing.T) {
	result := runScenario(t, fixtureScenario{after: func(peer *fixturePeer) error {
		if err := peer.notify("remoteControl/status/changed", fixtureRemoteControlDisabled()); err != nil {
			return err
		}
		return peer.terminal(fixtureTerminal())
	}})
	if result.err != nil || result.out.Status != "completed" || result.out.Completion.Kind != "technical_question" || result.callbacks != 1 {
		t.Fatal("later disabled-status report changed original managed completion")
	}
}

func TestRunTimestampedDisabledStatusUsesOriginalThreadBarrier(t *testing.T) {
	result := runScenario(t, fixtureScenario{before: func(method string, peer *fixturePeer) error {
		if method == "initialized" {
			return peer.enc.Encode(map[string]any{"method": "remoteControl/status/changed", "params": fixtureRemoteControlDisabled(), "emittedAtMs": int64(9007199254740993)})
		}
		return nil
	}, callback: func(context.Context, ThreadBinding) error { return ErrCallback }})
	requireFailure(t, result, ErrCallback)
	if result.callbacks != 1 || result.out.Binding.ThreadID != "thread-1" || result.out.TurnID != "" || !reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read", "thread/start"}) {
		t.Fatal("timestamped disabled status changed original binding-only barrier")
	}
}

func TestRunRemoteStatusCompatibilityDoesNotAdmitUnknownMethods(t *testing.T) {
	for _, tc := range []struct {
		name, at string
		request  bool
	}{{"notification", "initialized", false}, {"request", "initialized", true}, {"request_after_config_read", "config/read", true}} {
		t.Run(tc.name, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(method string, peer *fixturePeer) error {
				if method == tc.at {
					frame := map[string]any{"method": "remoteControl/enable", "params": fixtureRemoteControlDisabled()}
					if tc.request {
						frame["id"] = "synthetic-native-request"
					} else {
						frame["emittedAtMs"] = int64(1)
					}
					return peer.enc.Encode(frame)
				}
				return nil
			}})
			requireFailure(t, result, ErrProtocol)
			// An initialized Notify can finish before the incoming request is
			// queued. Its normal config/read may already be in flight; neither
			// prefix authorizes thread/start, a callback, a turn or remote RPC.
			prefix := reflect.DeepEqual(result.methods, []string{"initialize", "initialized"}) || reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read"})
			if result.callbacks != 0 || result.out.TurnID != "" || !prefix {
				t.Fatalf("unknown remote authority admitted: callbacks=%d turn_observed=%v methods=%v", result.callbacks, result.out.TurnID != "", result.methods)
			}
			if tc.at == "config/read" && len(result.methods) != 3 {
				t.Fatal("actual config/read injection boundary missing")
			}
		})
	}
}

func TestRunConsumesPinnedDisabledNotificationBeforeBindingOnlyCallback(t *testing.T) {
	result := runScenario(t, fixtureScenario{
		before: func(method string, peer *fixturePeer) error {
			if method == "initialized" {
				// Isolate M3's allowlist from M1's independently failing timestamp
				// envelope. EmittedAtMs is optional in the fixed native contract.
				return peer.notify("remoteControl/status/changed", fixtureRemoteControlDisabled())
			}
			return nil
		},
		callback: func(context.Context, ThreadBinding) error { return ErrCallback },
	})
	if !errors.Is(result.err, ErrCallback) || result.callbacks != 1 || result.out.Binding.ThreadID != "thread-1" || result.out.TurnID != "" || !reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read", "thread/start"}) {
		t.Fatalf("disabled native status prevented original binding-only callback: err=%v callbacks=%d methods=%v", result.err, result.callbacks, result.methods)
	}
}
