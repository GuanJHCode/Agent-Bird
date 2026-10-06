package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const compatibilityCWD = "/private/owned/project"

// The pinned Rust response has these optional experimental wire properties;
// the stable JSON generator omits them. Pure fixtures do not assert that the
// unrecorded native nested values actually had these defaults.
func fixtureExtendedThread() map[string]any {
	thread := fixtureThread()
	thread["environments"] = []any{map[string]any{"environmentId": "local", "cwd": compatibilityCWD, "runtimeWorkspaceRoots": []any{compatibilityCWD}}}
	thread["extra"], thread["canAcceptDirectInput"], thread["daybreakEnabled"] = nil, true, nil
	return thread
}

func fixtureExtendedThreadResponse(method string, response map[string]any) {
	if method != "thread/start" && method != "thread/resume" {
		return
	}
	response["runtimeWorkspaceRoots"] = []any{compatibilityCWD}
	response["multiAgentMode"], response["activePermissionProfile"] = "explicitRequestOnly", nil
	response["thread"] = fixtureExtendedThread()
	if method == "thread/resume" {
		response["initialTurnsPage"] = nil
	}
}

func TestRunPinnedThreadResponseCompatibility(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, when := range []string{"stable", "response_only", "early", "late", "early_and_late", "false_daybreak"} {
			t.Run(fmt.Sprintf("resume_%v/%s", resume, when), func(t *testing.T) {
				thread := fixtureExtendedThread()
				if when == "false_daybreak" {
					thread["daybreakEnabled"] = false
				}
				result := runScenario(t, fixtureScenario{resume: resume, mutate: func(method string, response map[string]any) {
					if when != "stable" {
						fixtureExtendedThreadResponse(method, response)
						if method == "thread/start" || method == "thread/resume" {
							response["thread"] = thread
						}
					}
				}, before: func(method string, p *fixturePeer) error {
					if (when == "early" || when == "early_and_late") && (method == "thread/start" || method == "thread/resume") {
						return p.notify("thread/started", map[string]any{"thread": thread})
					}
					if (when == "late" || when == "early_and_late") && method == "turn/start" {
						return p.notify("thread/started", map[string]any{"thread": thread})
					}
					return nil
				}})
				if result.err != nil || result.callbacks != 1 || result.out.Binding.ThreadID != "thread-1" || result.out.Completion.Text != "Which API?" {
					t.Fatalf("pinned wire format not accepted: %#v", result)
				}
			})
		}
	}
}

type threadCompatibilityFault struct {
	name       string
	threadOnly bool
	mutate     func(map[string]any)
	want       error
}

func fixtureThreadCompatibilityFaults() []threadCompatibilityFault {
	thread := func(response map[string]any) map[string]any { return obj(response["thread"]) }
	env := func(response map[string]any) map[string]any { return obj(arr(thread(response)["environments"])[0]) }
	return []threadCompatibilityFault{
		{"roots_null", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = nil }, ErrProtocol},
		{"roots_wrong_type", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = compatibilityCWD }, ErrProtocol},
		{"roots_empty", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = []any{} }, ErrPolicy},
		{"roots_duplicate", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = []any{compatibilityCWD, compatibilityCWD} }, ErrPolicy},
		{"roots_foreign", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = []any{"/private/foreign"} }, ErrPolicy},
		{"roots_uri", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = []any{"file://" + compatibilityCWD} }, ErrPolicy},
		{"roots_spelling", false, func(r map[string]any) { r["runtimeWorkspaceRoots"] = []any{compatibilityCWD + "/"} }, ErrPolicy},
		{"mode_null", false, func(r map[string]any) { r["multiAgentMode"] = nil }, ErrProtocol},
		{"mode_proactive", false, func(r map[string]any) { r["multiAgentMode"] = "proactive" }, ErrPolicy},
		{"mode_custom", false, func(r map[string]any) { r["multiAgentMode"] = "custom" }, ErrPolicy},
		{"profile_nonnull", false, func(r map[string]any) { r["activePermissionProfile"] = map[string]any{"id": ":readOnly"} }, ErrProtocol},
		{"environment_null", true, func(r map[string]any) { thread(r)["environments"] = nil }, ErrPolicy},
		{"environment_wrong_type", true, func(r map[string]any) { thread(r)["environments"] = false }, ErrProtocol},
		{"environment_empty", true, func(r map[string]any) { thread(r)["environments"] = []any{} }, ErrPolicy},
		{"environment_extra", true, func(r map[string]any) { e := env(r); thread(r)["environments"] = []any{e, e} }, ErrPolicy},
		{"environment_remote", true, func(r map[string]any) { env(r)["environmentId"] = "remote" }, ErrPolicy},
		{"environment_id_null", true, func(r map[string]any) { env(r)["environmentId"] = nil }, ErrProtocol},
		{"environment_cwd_foreign", true, func(r map[string]any) { env(r)["cwd"] = "/private/foreign" }, ErrPolicy},
		{"environment_cwd_uri", true, func(r map[string]any) { env(r)["cwd"] = "file://" + compatibilityCWD }, ErrPolicy},
		{"environment_root_foreign", true, func(r map[string]any) { env(r)["runtimeWorkspaceRoots"] = []any{"/private/foreign"} }, ErrPolicy},
		{"environment_roots_empty", true, func(r map[string]any) { env(r)["runtimeWorkspaceRoots"] = []any{} }, ErrPolicy},
		{"environment_roots_null", true, func(r map[string]any) { env(r)["runtimeWorkspaceRoots"] = nil }, ErrProtocol},
		{"environment_unknown", true, func(r map[string]any) { env(r)["newAuthority"] = nil }, ErrProtocol},
		{"environment_missing", true, func(r map[string]any) { delete(env(r), "cwd") }, ErrProtocol},
		{"environment_duplicate", true, func(r map[string]any) {
			thread(r)["environments"] = []any{json.RawMessage(`{"environmentId":"local","environmentId":"local","cwd":"/private/owned/project","runtimeWorkspaceRoots":["/private/owned/project"]}`)}
		}, ErrProtocol},
		{"environment_unicode", true, func(r map[string]any) {
			thread(r)["environments"] = []any{json.RawMessage(`{"environmentId":"local","cwd":"\ud800","runtimeWorkspaceRoots":["/private/owned/project"]}`)}
		}, ErrProtocol},
		{"extra_nonnull", true, func(r map[string]any) { thread(r)["extra"] = map[string]any{} }, ErrProtocol},
		{"direct_false", true, func(r map[string]any) { thread(r)["canAcceptDirectInput"] = false }, ErrPolicy},
		{"direct_null", true, func(r map[string]any) { thread(r)["canAcceptDirectInput"] = nil }, ErrPolicy},
		{"direct_wrong_type", true, func(r map[string]any) { thread(r)["canAcceptDirectInput"] = "true" }, ErrProtocol},
		{"daybreak_true", true, func(r map[string]any) { thread(r)["daybreakEnabled"] = true }, ErrPolicy},
		{"daybreak_wrong_type", true, func(r map[string]any) { thread(r)["daybreakEnabled"] = 0 }, ErrProtocol},
		{"thread_unknown", true, func(r map[string]any) { thread(r)["experimentalEscape"] = nil }, ErrProtocol},
		{"thread_cwd_foreign", true, func(r map[string]any) { thread(r)["cwd"] = "/private/foreign" }, ErrPolicy},
		{"readonly_guard", false, func(r map[string]any) { obj(r["sandbox"])["networkAccess"] = true }, ErrPolicy},
	}
}

func TestRunPinnedThreadResponseRejectsUnsupportedBindings(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, fault := range fixtureThreadCompatibilityFaults() {
			t.Run(fmt.Sprintf("resume_%v/%s", resume, fault.name), func(t *testing.T) {
				result := runScenario(t, fixtureScenario{resume: resume, mutate: func(method string, response map[string]any) {
					if method == "thread/start" || method == "thread/resume" {
						fixtureExtendedThreadResponse(method, response)
						fault.mutate(response)
					}
				}})
				requireFailure(t, result, fault.want)
				if result.callbacks != 0 || strings.Contains(strings.Join(result.methods, ","), "turn/start") {
					t.Fatal("rejected response crossed callback/turn barrier")
				}
			})
			if fault.threadOnly {
				t.Run(fmt.Sprintf("resume_%v/early_%s", resume, fault.name), func(t *testing.T) {
					result := runScenario(t, fixtureScenario{resume: resume, before: func(method string, p *fixturePeer) error {
						if method == "thread/start" || method == "thread/resume" {
							r := fixtureResponse(method)
							fixtureExtendedThreadResponse(method, r)
							fault.mutate(r)
							return p.notify("thread/started", map[string]any{"thread": r["thread"]})
						}
						return nil
					}})
					requireFailure(t, result, fault.want)
					if result.callbacks != 0 || strings.Contains(strings.Join(result.methods, ","), "turn/start") {
						t.Fatal("rejected early notification crossed callback/turn barrier")
					}
				})
			}
		}
	}
	for _, page := range []any{map[string]any{}, []any{}, false, "page"} {
		t.Run(fmt.Sprintf("resume_page_%T", page), func(t *testing.T) {
			result := runScenario(t, fixtureScenario{resume: true, mutate: func(method string, r map[string]any) {
				if method == "thread/resume" {
					fixtureExtendedThreadResponse(method, r)
					r["initialTurnsPage"] = page
				}
			}})
			requireFailure(t, result, ErrProtocol)
			if result.callbacks != 0 {
				t.Fatal("unsupported history page accepted")
			}
		})
	}
}

func TestRunPinnedThreadCompatibilityKeepsDuplicateFingerprint(t *testing.T) {
	result := runScenario(t, fixtureScenario{before: func(method string, p *fixturePeer) error {
		if method != "thread/start" {
			return nil
		}
		thread := fixtureExtendedThread()
		if err := p.notify("thread/started", map[string]any{"thread": thread}); err != nil {
			return err
		}
		thread["daybreakEnabled"] = false
		return p.notify("thread/started", map[string]any{"thread": thread})
	}})
	if !errors.Is(result.err, ErrProtocol) || result.callbacks != 0 {
		t.Fatal("different duplicate thread fingerprint accepted")
	}
}

func TestRunPinnedThreadLateScopeFailureJoinsBinding(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"environment", func(thread map[string]any) { obj(arr(thread["environments"])[0])["cwd"] = "/private/foreign" }, ErrPolicy},
		{"direct", func(thread map[string]any) { thread["canAcceptDirectInput"] = false }, ErrPolicy},
		{"daybreak", func(thread map[string]any) { thread["daybreakEnabled"] = true }, ErrPolicy},
		{"unknown", func(thread map[string]any) { thread["unknownAuthority"] = nil }, ErrProtocol},
		{"foreign_id", func(thread map[string]any) { thread["id"] = "foreign" }, ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := make(chan *fixturePeer, 1)
			joined := make(chan struct{})
			result := runScenario(t, fixtureScenario{mutate: fixtureExtendedThreadResponse, before: func(method string, p *fixturePeer) error {
				if method == "thread/start" {
					peer <- p
				}
				return nil
			}, callback: func(ctx context.Context, _ ThreadBinding) error {
				defer close(joined)
				thread := fixtureExtendedThread()
				test.mutate(thread)
				// The matched binding is already valid. Hold its original callback
				// while a later wire notification reaches Run's sole consumer.
				if err := (<-peer).notify("thread/started", map[string]any{"thread": thread}); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}})
			requireFailure(t, result, test.want)
			if result.callbacks != 1 || strings.Contains(strings.Join(result.methods, ","), "turn/start") {
				t.Fatal("late failure crossed held callback/turn barrier")
			}
			select {
			case <-joined:
			default:
				t.Fatal("original binding callback not joined")
			}
		})
	}
}
