//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Real private ExecuteLaunch/FS/spool consumer; no OS child is possible. The
// test owns the missing pin's parent. startCommand verifies this missing pin
// before cmd.Start (process.go), so an old admission predicate can reach only
// the prefork binary_pin_mismatch branch. This is not original-Wait evidence.
func codexHardlinkFixture(t *testing.T, resume bool) (*Host, contract.LaunchCommand, adapter.Invocation, []string) {
	t.Helper()
	h, g, _, env := codexOriginFixture(t)
	var payload adapter.InvocationPayload
	if json.Unmarshal(g.AdapterPayload, &payload) != nil {
		t.Fatal("payload fixture")
	}
	payload.ProviderLock.Binary.Path = filepath.Join(payload.Directory, "missing-owned-codex")
	if _, err := os.Lstat(payload.ProviderLock.Binary.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exclusive fixture pin must be absent")
	}
	g.AdapterPayload, _ = json.Marshal(payload)
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: payload.ProviderLock.Binary, Lock: payload.ProviderLock, Profile: payload.Profile, CWD: payload.Directory, Prompt: payload.Prompt})
	if err != nil {
		t.Fatal(err)
	}
	if !resume {
		return h, g, inv, env
	}
	// Synthetic source Session/Question/Exited and a directly written seal,
	// identical to other pure restore fixtures; never manufacture a Handle.
	ctx := context.Background()
	p, err := h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	_, p, err = codexManagedCommand(ctx, p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	id := process.Identity{PID: 3456, PGID: 3456, Birth: "synthetic-hardlink-source", BirthKnown: true, Executable: p.Launch.Path, ExecutableSHA256: p.Launch.SHA256}
	meta := launchMetadata{commandID: g.CommandID, workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
		if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	b := codexsession.ThreadBinding{ThreadID: "12345678-1234-4321-8123-123456789abc", Model: "gpt-fixed", ModelProvider: "openai", HistoryMode: "paginated"}
	b.RolloutPath = filepath.Join(p.Layout.Sessions.Path, "2026/10/05/rollout-2026-10-05T01-02-03-"+b.ThreadID+".jsonl")
	bound, err := h.bindManagedCodexThread(ctx, p, g, id, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range codexSQLiteFamilies {
		body := make([]byte, 100)
		copy(body, "SQLite format 3\x00")
		if os.WriteFile(filepath.Join(p.Layout.SQLite.Path, family+".sqlite"), body, 0644) != nil {
			t.Fatal("opaque SQLite fixture")
		}
	}
	if os.MkdirAll(filepath.Dir(b.RolloutPath), 0755) != nil || os.WriteFile(b.RolloutPath, []byte("{}\n"), 0644) != nil {
		t.Fatal("opaque rollout fixture")
	}
	candidate, err := h.captureManagedCodexQuestionSeal(ctx, p, g, bound, 1, hashText("Which file?"), codexStateReadControl{}, codexSealControl{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := codexEncodeSeal(candidate.Record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexWriteProtectedRecord(ctx, p.Protected, "codex-session-seal.json", body); err != nil {
		t.Fatal(err)
	}
	zero := 0
	q, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventQuestion, hashText("Which file?"), &id, &zero, nil, structuredEventFields{SessionKind: "session-id", SessionID: b.ThreadID, QuestionRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventExited, hashText("exited"), &id, &zero, nil, structuredEventFields{ActiveMS: 10}); err != nil {
		t.Fatal(err)
	}
	g.Continuation = &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: g.SegmentID, SourceEventID: q.EventID, SourceEventRevision: q.Sequence, SourceEventSHA256: q.PayloadHash, SessionEventID: bound.Session.EventID, AdapterPayloadSHA256: codexDigest(g.AdapterPayload), OwnerContextID: "owner"}
	g.SegmentID, g.CommandID, g.ExecutionEpoch = "second", "second-command", 2
	g.QuestionID, g.QuestionRevision, g.Answer, g.SessionKind, g.SessionID = q.QuestionID, 1, "Read the authorized file.", "session-id", b.ThreadID
	inv, err = adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p.Origin.Lock.Binary, Lock: &p.Origin.Lock, Profile: &p.Origin.Profile, CWD: p.Origin.Workspace, Prompt: g.Answer, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}, Continuation: g.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	return h, g, inv, env
}

func codexHardlinkEnvironment(t *testing.T, env []string) {
	t.Helper()
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "HOME" || key == "CODEX_HOME") {
			t.Setenv(key, value)
		}
	}
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
}

func TestCodexHardlinksRejectedAtActualBeforeStart(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "restore"}[resume], func(t *testing.T) {
			for _, relative := range []string{"home/sessions", "home/skills", "home/tmp", "home/thread-writer-locks", "sqlite", "log", "tmp"} {
				t.Run(strings.ReplaceAll(relative, "/", "_"), func(t *testing.T) {
					h, g, inv, env := codexHardlinkFixture(t, resume)
					codexHardlinkEnvironment(t, env)
					g.ReservationID, g.GrantedActiveMS = "reservation", 60000
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					reached := false
					source := filepath.Join(inv.WorkingDirectory(), "synthetic-link-source")
					if os.WriteFile(source, []byte("SYNTHETIC_ONLY\n"), 0600) != nil {
						t.Fatal("source fixture")
					}
					result, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{beforeStart: func() {
						reached = true
						var payload adapter.InvocationPayload
						_ = json.Unmarshal(g.AdapterPayload, &payload)
						if _, pinErr := os.Lstat(payload.ProviderLock.Binary.Path); !errors.Is(pinErr, os.ErrNotExist) {
							t.Fatal("owned pin ceased to be missing; must not run launcher")
						}
						state := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state")
						if err := os.Link(source, filepath.Join(state, filepath.FromSlash(relative), "prestart-hardlink")); err != nil {
							t.Fatal("actual beforeStart link injection", err)
						}
					}})
					spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
					events, readErr := spool.Read()
					if !reached || err == nil || readErr != nil || result.Status != "failed" || len(events) != 2 || events[0].Kind != contract.EventPrepared || events[1].Kind != contract.EventLaunchRejected || events[1].Process != nil || len(h.codexLeases) != 0 || len(h.active) != 0 {
						t.Fatalf("missing final inventory rejection: reached=%t result=%s error=%v events=%v leases=%d active=%d", reached, result.Status, err, codexHardlinkEventKinds(events), len(h.codexLeases), len(h.active))
					}
					if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("prestart rejection fabricated a resumable seal")
					}
				})
			}
		})
	}
}
func codexHardlinkEventKinds(events []contract.Event) []string {
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func TestCodexHardlinkLegitimateInitialReachesOnlyMissingPinPrefork(t *testing.T) {
	h, g, inv, env := codexHardlinkFixture(t, false)
	codexHardlinkEnvironment(t, env)
	g.ReservationID, g.GrantedActiveMS = "reservation", 60000
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reached := false
	result, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{beforeStart: func() { reached = true }})
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	events, readErr := spool.Read()
	if !reached || !errors.Is(err, process.CodeError("binary_pin_mismatch")) || result.Status != "unknown" || readErr != nil || len(events) != 2 || events[0].Kind != contract.EventPrepared || events[1].Kind != contract.EventUnknown || events[1].Process != nil || len(h.codexLeases) != 1 || len(h.active) != 0 {
		t.Fatal("legitimate initial path did not reach only controlled prefork pin rejection", reached, result.Status, err, codexHardlinkEventKinds(events))
	}
	for _, lease := range h.codexLeases {
		if lease.handle != nil {
			t.Fatal("missing pin fabricated an original Handle")
		}
	}
}

func TestCodexHardlinkRulesUseSeparateOperationWithoutExpandingScope(t *testing.T) {
	h, g, inv, env := codexHardlinkFixture(t, false)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil || len(cmd.Args) < 4 || strings.Count(cmd.Args[1], "(deny file-link)") != 1 || strings.Count(cmd.Args[1], "(allow file-write* (subpath ") != 7 || !strings.Contains(cmd.Args[1], "(deny file-write*)") || !strings.Contains(cmd.Args[1], "(deny file-read* (subpath ") || cmd.Path != "/usr/bin/sandbox-exec" || cmd.PinnedPath != p.Origin.Lock.Binary.Path || cmd.PinnedSHA256 != p.Origin.Lock.Binary.SHA256 {
		t.Fatal("Codex builder lacks separate hardlink denial or changed scope/pin")
	}
}
