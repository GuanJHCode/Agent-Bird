package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const fixtureRolloutPath = "/private/owned/codex/sessions/2026/10/05/rollout-2026-10-05T12-00-00-thread-1.jsonl"

// Real M1/net.Pipe; no filesystem/provider/actor. Host separately verifies the
// exact UUID, canonical owned path, original origin and physical state seal.
func TestThreadBindingProjectsPersistenceBeforeCallback(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "resume"}[resume], func(t *testing.T) {
			result := runScenario(t, fixtureScenario{resume: resume, mutate: func(method string, response map[string]any) {
				if method == "thread/start" || method == "thread/resume" {
					thread := obj(response["thread"])
					thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
				}
			}, callback: func(_ context.Context, binding ThreadBinding) error {
				raw, err := json.Marshal(binding)
				if err != nil {
					t.Error(err)
				}
				var projected map[string]any
				_ = json.Unmarshal(raw, &projected)
				if projected["RolloutPath"] != fixtureRolloutPath || projected["HistoryMode"] != "paginated" {
					t.Error("trusted callback lacks exact native persistence identity")
				}
				return ErrCallback // Durable Host binding is not authorized by this pure test.
			}})
			if !errors.Is(result.err, ErrCallback) || result.callbacks != 1 || strings.Contains(strings.Join(result.methods, ","), "turn/start") {
				t.Fatalf("projection crossed callback barrier: %#v", result)
			}
		})
	}
}

func TestThreadPersistenceResponseAndNotificationMustAgree(t *testing.T) {
	for _, field := range []string{"path", "historyMode"} {
		t.Run(field, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{mutate: func(method string, response map[string]any) {
				if method == "thread/start" {
					thread := obj(response["thread"])
					thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
				}
			}, before: func(method string, peer *fixturePeer) error {
				if method == "thread/start" {
					thread := fixtureThread()
					thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
					if field == "path" {
						thread["path"] = fixtureRolloutPath + ".foreign"
					} else {
						thread["historyMode"] = "legacy"
					}
					return peer.notify("thread/started", map[string]any{"thread": thread})
				}
				return nil
			}})
			requireFailure(t, result, ErrProtocol)
			if result.callbacks != 0 || strings.Contains(strings.Join(result.methods, ","), "turn/start") {
				t.Fatal("conflicting native persistence crossed the binding barrier")
			}
		})
	}
}

func TestThreadPersistenceLateNotificationCannotReplaceBinding(t *testing.T) {
	for _, field := range []string{"path", "historyMode"} {
		t.Run(field, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{mutate: func(method string, response map[string]any) {
				if method == "thread/start" {
					thread := obj(response["thread"])
					thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
				}
			}, before: func(method string, peer *fixturePeer) error {
				if method == "turn/start" {
					thread := fixtureThread()
					thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
					thread[field] = nil
					return peer.notify("thread/started", map[string]any{"thread": thread})
				}
				return nil
			}})
			requireFailure(t, result, ErrProtocol)
			if result.callbacks != 1 {
				t.Fatal("late failure must cancel/join the original bound session")
			}
		})
	}
}

func TestThreadPersistenceIdenticalNotificationsKeepOriginalProjection(t *testing.T) {
	thread := fixtureThread()
	thread["path"], thread["historyMode"] = fixtureRolloutPath, "paginated"
	result := runScenario(t, fixtureScenario{mutate: func(method string, response map[string]any) {
		if method == "thread/start" {
			response["thread"] = thread
		}
	}, before: func(method string, peer *fixturePeer) error {
		if method == "thread/start" || method == "turn/start" {
			return peer.notify("thread/started", map[string]any{"thread": thread})
		}
		return nil
	}})
	if result.err != nil || result.callbacks != 1 || result.out.Completion.Kind != "technical_question" || result.out.Binding.RolloutPath != fixtureRolloutPath || result.out.Binding.HistoryMode != "paginated" {
		t.Fatalf("identical early/late identity must remain bound: %#v", result)
	}
}
