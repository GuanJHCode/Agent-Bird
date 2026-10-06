//go:build darwin

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

const codexActorOptIn = "root-reviewed-c06-managed-retained-pipe-v1"
const codexActorProviderPath = "/private/fixture/native/codex"
const codexActorThreadID = "12345678-1234-4321-8123-123456789abc"

// Both explicit selectors are required before any executable/file-source
// inspection. No TestMain/init launches anything. These tests use the private
// Go entry, real kernel/sandbox/Wait and synthetic provider frames/state; they
// do not execute Codex or establish its persistence/model/tool behavior.
func TestManagedCodexRetainedPipeActor(t *testing.T) {
	if os.Getenv("C06_MANAGED_ACTOR_OPT_IN") != codexActorOptIn || os.Getenv("C06_MANAGED_ACTOR_EVIDENCE_DIR") == "" {
		t.Skip("ROOT reviewed double opt-in retained-pipe actor")
	}
	evidence := os.Getenv("C06_MANAGED_ACTOR_EVIDENCE_DIR")
	root, err := codexAnchorStateRoot(context.Background(), evidence)
	if err != nil || len(root.Parents) == 0 || root.Parents[len(root.Parents)-1].Mode != 0700 {
		t.Fatal("canonical original owned evidence directory required")
	}
	for _, name := range []string{"two_questions", "malformed", "permission", "segment_stop", "stop_all", "first_canceled_unknown"} {
		if !t.Run(name, func(t *testing.T) { codexRunRetainedPipeActor(t, root, name) }) {
			// Includes unproved original Wait/tree/Execute join. Do not launch
			// another case after a failed lifecycle/cleanup assertion.
			t.Fatal("actor case failed; subsequent cases not started")
		}
	}
}

// Child entry is only the trusted argv tail, not a production env switch.
// Exit directly so Go testing never writes PASS text into the JSON-RPC pipe.
func TestManagedCodexActorHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-2] != "codex-managed-actor" {
		t.Skip("owned actor argv only")
	}
	if os.Getenv("C06_MANAGED_ACTOR_OPT_IN") != codexActorOptIn {
		os.Exit(91)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Bounds even a blocked JSON decoder. This is only the original child
	// process watchdog, never a substitute for the parent's original Wait.
	watchdog := time.AfterFunc(20*time.Second, func() { os.Exit(93) })
	defer watchdog.Stop()
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(92)
	}
	if err := codexActorProtocol(ctx, os.Stdin, os.Stdout, os.Getenv("CODEX_HOME"), cwd, os.Args[len(os.Args)-1], os.Getpid()); err != nil {
		os.Exit(92)
	}
	os.Exit(0)
}

func codexActorMarker(home string, pid int, phase string) string {
	return filepath.Join(home, "tmp", fmt.Sprintf("actor-%d-%s", pid, phase))
}

// Marker contents are fixed nonsecret phase labels. They live only in the
// already authorized runtime root and become part of the real auxiliary seal.
func codexActorWriteMarker(home string, pid int, phase string) error {
	path := codexActorMarker(home, pid, phase)
	// The reader must never observe O_EXCL's empty/newly written inode. Publish
	// only complete fsynced bytes, with no replacement of an existing release.
	pending := path + ".pending"
	if err := codexActorWriteFile(pending, []byte(phase+"\n"), os.O_EXCL); err != nil {
		return err
	}
	if err := unix.RenameatxNp(unix.AT_FDCWD, pending, unix.AT_FDCWD, path, unix.RENAME_EXCL); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func codexActorAwaitMarker(ctx context.Context, home string, pid int, phase string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path := codexActorMarker(home, pid, phase)
		info, err := os.Lstat(path)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() != int64(len(phase)+1) {
				return errCodexStateFile
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != phase+"\n" {
				return errCodexStateFile
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func codexActorWriteFile(path string, body []byte, flags int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|flags, 0600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(body)
	if n != len(body) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func codexActorThread(home, cwd string) map[string]any {
	return map[string]any{"id": codexActorThreadID, "preview": "", "ephemeral": false, "modelProvider": "openai", "createdAt": 1, "updatedAt": 1, "status": map[string]any{"type": "idle"}, "path": codexActorRollout(home), "historyMode": "paginated", "cwd": cwd, "cliVersion": "0.154.0", "source": "appServer", "sessionId": "synthetic-session", "projectId": nil, "turns": []any{}}
}

func codexActorRollout(home string) string {
	return filepath.Join(home, "sessions/2026/10/05/rollout-2026-10-05T01-02-03-"+codexActorThreadID+".jsonl")
}

func codexActorPersistSyntheticState(home, scenario string) error {
	state := filepath.Dir(home)
	for _, family := range codexSQLiteFamilies {
		body := make([]byte, 100)
		copy(body, "SQLite format 3\x00")
		copy(body[20:], scenario)
		if err := codexActorWriteFile(filepath.Join(state, "sqlite", family+".sqlite"), body, os.O_TRUNC); err != nil {
			return err
		}
	}
	rollout := codexActorRollout(home)
	if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
		return err
	}
	// Deliberately opaque fixture bytes, never a SQLite/real-Codex claim.
	return codexActorWriteFile(rollout, []byte("{\"synthetic\":true}\n"), os.O_APPEND)
}

// Same functions generate the actual child frames and the pure net.Pipe peer.
// No source config/auth/installation file is read here; paths are derived from
// the existing CODEX_HOME and synthetic public fixture defaults only.
func codexActorProtocol(ctx context.Context, input io.Reader, output io.Writer, home, cwd, scenario string, pid int) (returnedErr error) {
	if !codexActorScenario(scenario) || !codexCanonicalPath(home) || !codexCanonicalPath(cwd) || filepath.Base(home) != "home" || pid < 1 {
		return errCodexStateFile
	}
	phase := "initialize"
	defer func() {
		if returnedErr != nil {
			body, _ := json.Marshal(map[string]any{"phase": phase, "error_class": codexActorErrorClass(returnedErr)})
			// Closed labels only, never RPC/source/auth values. Missing
			// diagnostics cannot change protocol or process proof.
			_ = codexActorWriteFile(codexActorMarker(home, pid, "helper-failure"), append(body, '\n'), os.O_EXCL)
		}
	}()
	dec, enc := json.NewDecoder(input), json.NewEncoder(output)
	method := func(expected string) (map[string]json.RawMessage, error) {
		var frame map[string]json.RawMessage
		if err := dec.Decode(&frame); err != nil {
			return nil, err
		}
		var actual string
		if json.Unmarshal(frame["method"], &actual) != nil || actual != expected {
			return nil, errCodexStateFile
		}
		return frame, nil
	}
	frame, err := method("initialize")
	if err != nil || codexActorWriteMarker(home, pid, "initialize-ready") != nil || codexActorAwaitMarker(ctx, home, pid, "initialize-release") != nil {
		return errCodexStateFile
	}
	if err := enc.Encode(map[string]any{"id": frame["id"], "result": map[string]any{"codexHome": home, "platformFamily": "unix", "platformOs": "macos", "userAgent": "owned-synthetic-actor"}}); err != nil {
		return err
	}
	phase = "initialized"
	if _, err = method("initialized"); err != nil {
		return err
	}
	phase = "config_read"
	frame, err = method("config/read")
	if err != nil {
		return err
	}
	p := codexPreparedState{Layout: codexStateLayout{Home: codexStateRoot{Path: home}, SQLite: codexStateRoot{Path: filepath.Join(filepath.Dir(home), "sqlite")}}}
	p.Layout.Auxiliary.Runtime[3].Path = filepath.Join(filepath.Dir(home), "log")
	if err = enc.Encode(map[string]any{"id": frame["id"], "result": map[string]any{"config": codexThreadEffectiveFixture(p), "origins": map[string]any{}}}); err != nil {
		return err
	}
	phase = "thread"
	if err := dec.Decode(&frame); err != nil {
		return err
	}
	var threadMethod string
	if json.Unmarshal(frame["method"], &threadMethod) != nil || threadMethod != "thread/start" && threadMethod != "thread/resume" {
		return errCodexStateFile
	}
	if threadMethod == "thread/resume" {
		var params map[string]any
		if json.Unmarshal(frame["params"], &params) != nil || params["threadId"] != codexActorThreadID || params["excludeTurns"] != true {
			return errCodexStateFile
		}
	}
	threadResult := map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd, "model": "gpt-fixed", "modelProvider": "openai", "reasoningEffort": "source-effort", "serviceTier": nil, "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": codexActorThread(home, cwd)}
	if err = enc.Encode(map[string]any{"id": frame["id"], "result": threadResult}); err != nil {
		return err
	}
	phase = "turn"
	frame, err = method("turn/start")
	if err != nil || codexActorWriteMarker(home, pid, "turn-ready") != nil || codexActorAwaitMarker(ctx, home, pid, "turn-release") != nil {
		return errCodexStateFile
	}
	if scenario == "hold" || scenario == "unknown" {
		phase = "held_exit"
		// Ignore pipe EOF until the parent's owned release. In the UNKNOWN case
		// the original canceled no-deadline stop cannot signal a fresh window.
		return codexActorAwaitMarker(ctx, home, pid, "exit-release")
	}
	if scenario == "malformed" {
		_, err := io.WriteString(output, "{malformed-synthetic-frame}\n")
		if err != nil {
			return err
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return errCodexStateFile
		}
		return nil
	}
	if scenario == "permission" {
		phase = "permission"
		requestID := "owned-permission"
		params := map[string]any{"threadId": codexActorThreadID, "turnId": "actor-turn", "itemId": "permission", "startedAtMs": 2}
		if err := enc.Encode(map[string]any{"id": requestID, "method": "item/commandExecution/requestApproval", "params": params}); err != nil {
			return err
		}
		var answer map[string]json.RawMessage
		if dec.Decode(&answer) != nil || string(answer["id"]) != `"owned-permission"` || string(answer["result"]) != `{"decision":"decline"}` {
			return errCodexStateFile
		}
		if err := enc.Encode(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": codexActorThreadID, "requestId": requestID}}); err != nil {
			return err
		}
		// Early denial must still reconcile this original pending reply. M3
		// refuses its permission flag when binding the actual turn response.
		if err := enc.Encode(map[string]any{"id": frame["id"], "result": map[string]any{"turn": map[string]any{"id": "actor-turn", "items": []any{}, "status": "inProgress", "startedAt": 2}}}); err != nil {
			return err
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return errCodexStateFile
		}
		return nil
	}
	phase = "completion"
	if err := codexActorPersistSyntheticState(home, scenario); err != nil {
		return err
	}
	turn := map[string]any{"id": "actor-turn", "items": []any{}, "status": "inProgress", "startedAt": 2}
	if err := enc.Encode(map[string]any{"id": frame["id"], "result": map[string]any{"turn": turn}}); err != nil {
		return err
	}
	kind, text := "technical_question", "Which authorized file?"
	if scenario == "result" {
		kind, text = "result", "SYNTHETIC_ACTOR_FINAL"
	}
	completion, _ := json.Marshal(map[string]any{"version": 1, "kind": kind, "text": text})
	item := map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": string(completion)}
	if err := enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": codexActorThreadID, "turn": map[string]any{"id": "actor-turn", "status": "completed", "items": []any{item}, "startedAt": 2, "completedAt": 3, "error": nil}}}); err != nil {
		return err
	}
	phase = "await_close"
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errCodexStateFile
	}
	return nil
}

func codexActorErrorClass(err error) string {
	for _, known := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"},
		{codexsession.ErrProtocol, "protocol"}, {codexsession.ErrPermission, "permission"},
		{codexsession.ErrCallback, "callback"}, {codexsession.ErrPolicy, "policy"},
		{process.ErrProcessTreeUnknown, "tree_unknown"}, {errCodexStateFile, "fixture_state"},
		{io.EOF, "eof"}, {io.ErrClosedPipe, "pipe_closed"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	if err == nil {
		return "none"
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "filesystem"
	}
	return "other"
}

type codexActorSegmentEvidence struct {
	ErrorClass                           string
	Segment, Status                      string
	PID, PGID, ExitCode                  int
	BirthKnown, OriginalWait, TreeAbsent bool
	SessionBeforeTurn, SealPresent       bool
	UnknownRetainedBeforeRelease         bool
	StderrBytes                          int
	StderrSHA256                         string
	EventKinds                           []string
}

func codexRunRetainedPipeActor(t *testing.T, evidence codexStateRoot, name string) {
	t.Helper()
	base := filepath.Join(evidence.Path, name)
	if os.Mkdir(base, 0700) != nil {
		t.Fatal("fresh leaf directory required; no reuse/retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	for _, dir := range []string{"source", "workspace", "spool"} {
		if os.Mkdir(filepath.Join(base, dir), 0700) != nil {
			t.Fatal("owned fixture setup")
		}
	}
	source, cwd, spoolRoot := filepath.Join(base, "source"), filepath.Join(base, "workspace"), filepath.Join(base, "spool")
	if codexActorWriteFile(filepath.Join(source, "config.toml"), []byte("model=\"gpt-fixed\"\nmodel_reasoning_effort=\"source-effort\"\n"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(source, "auth.json"), []byte("SYNTHETIC_AUTH_NEVER_READ"), os.O_EXCL) != nil {
		t.Fatal("synthetic config/auth fixture setup")
	}
	t.Setenv("HOME", base)
	t.Setenv("CODEX_HOME", source)
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
	helper, err := os.Executable()
	if err == nil {
		helper, err = filepath.EvalSymlinks(helper)
	}
	if err != nil {
		t.Fatal("canonical actual helper executable required")
	}
	helperSHA, err := process.ExecutableDigest(ctx, helper)
	if err != nil {
		t.Fatal("helper digest")
	}
	providerSHA, err := process.ExecutableDigest(ctx, codexActorProviderPath)
	if err != nil || providerSHA != adapter.CodexSHA256 {
		t.Fatal("original provider pin must independently match; no provider execution")
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: adapter.BinaryPin{Path: codexActorProviderPath, SHA256: adapter.CodexSHA256, Version: adapter.CodexVersion}}
	payload, _ := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: cwd, Prompt: "synthetic readonly task", Profile: &profile, ProviderLock: &lock})
	g := contract.LaunchCommand{RunID: "actor-run", TaskID: "actor-task", AttemptID: "actor-attempt", SegmentID: "segment-1", WorkRevision: 1, PlanRevision: 1, CommandID: "command-1", HostID: "actor-host", ExecutionEpoch: 1, ReservationID: "reservation-1", GrantedActiveMS: 60000, AdapterPayload: payload}
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: lock.Binary, Lock: &lock, Profile: &profile, CWD: cwd, Prompt: "synthetic readonly task"})
	if err != nil {
		t.Fatal("private invocation fixture")
	}
	var segments []codexActorSegmentEvidence
	fdBefore, fdBeforeErr := os.ReadDir("/dev/fd")
	defer func() {
		fdAfter, fdAfterErr := os.ReadDir("/dev/fd")
		body, _ := json.MarshalIndent(map[string]any{"version": 1, "name": name, "public_entry": false, "synthetic_provider_protocol_and_state": true, "codex_executed": false, "model_turns": 0, "segments": segments, "fd_observed": fdBeforeErr == nil && fdAfterErr == nil, "fd_before": len(fdBefore), "fd_after": len(fdAfter), "fd_verdict": "observation_only_unisolated_go_runtime"}, "", "  ")
		if err := codexActorWriteFile(filepath.Join(base, "actor-evidence.json"), append(body, '\n'), os.O_EXCL); err != nil {
			t.Error("evidence retained write failed")
		}
	}()
	cycles := 1
	if name == "two_questions" {
		cycles = 3
	}
	initialSession := ""
	for cycle := 0; cycle < cycles; cycle++ {
		h, err := NewIPC(spoolRoot, "actor-producer")
		if err != nil || h.BindContinuationOwner("actor-owner") != nil {
			t.Fatal("cold original Host owner")
		}
		scenario := "question"
		switch name {
		case "malformed", "permission":
			scenario = name
		case "segment_stop", "stop_all":
			scenario = "hold"
		case "first_canceled_unknown":
			scenario = "unknown"
		}
		if cycle == 2 {
			scenario = "result"
		}
		records, result, runErr, observation := codexActorOneSegment(t, ctx, h, g, inv, codexActorCommand{Path: helper, SHA256: helperSHA, Scenario: scenario}, name)
		segments = append(segments, observation)
		if name != "two_questions" {
			if name == "malformed" || name == "permission" {
				if runErr == nil || result.Status != "failed" || codexActorHasEvent(records, contract.EventQuestion) || codexActorHasEvent(records, contract.EventResult) || !codexActorHasEvent(records, contract.EventFailed) {
					t.Fatal("native protocol/permission failure became completion")
				}
			} else if name == "first_canceled_unknown" {
				if result.Status != "unknown" || !observation.UnknownRetainedBeforeRelease || codexActorHasEvent(records, contract.EventExited) || codexActorHasEvent(records, contract.EventQuestion) {
					t.Fatal("UNKNOWN dropped original responsibility or fabricated exit")
				}
			} else if result.Status != "interrupted" || !codexActorHasEvent(records, contract.EventStopped) || !codexActorHasEvent(records, contract.EventExited) || observation.SealPresent {
				t.Fatal("original bounded stop failed or created resumable seal")
			}
			break
		}
		if runErr != nil || !observation.OriginalWait || !observation.TreeAbsent || observation.ExitCode != 0 || !observation.SessionBeforeTurn {
			t.Fatal("successful segment lacks original physical proof or binding barrier")
		}
		if cycle == 2 {
			if result.Status != "result_ready" || !codexActorHasEvent(records, contract.EventResult) || observation.SealPresent {
				t.Fatal("final result routed incorrectly")
			}
			break
		}
		if result.Status != "waiting_user" || !observation.SealPresent {
			t.Fatal("real exit publisher did not seal technical question")
		}
		var question, session contract.Event
		for _, event := range records {
			if event.Kind == contract.EventQuestion {
				question = event
			}
			if event.Kind == contract.EventSession {
				session = event
			}
		}
		if cycle == 0 {
			initialSession = session.EventID
		}
		ref := &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: g.SegmentID, SourceEventID: question.EventID, SourceEventRevision: question.Sequence, SourceEventSHA256: question.PayloadHash, SessionEventID: initialSession, AdapterPayloadSHA256: codexDigest(g.AdapterPayload), OwnerContextID: "actor-owner"}
		g.SegmentID, g.CommandID, g.ReservationID = fmt.Sprintf("segment-%d", cycle+2), fmt.Sprintf("command-%d", cycle+2), fmt.Sprintf("reservation-%d", cycle+2)
		g.ExecutionEpoch++
		g.SessionKind, g.SessionID, g.QuestionID, g.QuestionRevision, g.Answer, g.Continuation = "session-id", codexActorThreadID, question.QuestionID, question.QuestionRevision, "next synthetic readonly answer", ref
		inv, err = adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: lock.Binary, Lock: &lock, Profile: &profile, CWD: cwd, Prompt: g.Answer, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}, Continuation: ref})
		if err != nil {
			t.Fatal("typed continuation fixture")
		}
	}
}

func codexActorHasEvent(events []contract.Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func codexActorOneSegment(t *testing.T, parentCtx context.Context, h *Host, g contract.LaunchCommand, inv adapter.Invocation, actor codexActorCommand, testCase string) ([]contract.Event, contract.Result, error, codexActorSegmentEvidence) {
	t.Helper()
	// Execution gets at most20s of the original40s parent. Cleanup/late attach
	// consumes that same parent's remaining bound, not a fresh failure grace.
	parentDeadline, bounded := parentCtx.Deadline()
	deadline, deadlineErr := codexActorExecutionDeadline(parentDeadline, time.Now())
	if !bounded || deadlineErr != nil {
		t.Fatal("original parent has no execution/cleanup budget; zero Start")
	}
	ctx, cancel := context.WithDeadline(parentCtx, deadline)
	home := filepath.Join(h.spoolRoot, g.AttemptID, "codex-state", "home")
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		cancel()
		t.Fatal("fresh segment spool")
	}
	type returned struct {
		result contract.Result
		err    error
	}
	done := make(chan returned, 1)
	consumed, identitySaved := false, false
	var p *process.Handle
	var lease *codexHandleLease
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	lookup := func() {
		h.stopMu.Lock()
		h.mu.Lock()
		lease = h.codexLeases[g.CommandID]
		if lease != nil && lease.handle != nil {
			p = lease.handle
		}
		h.mu.Unlock()
		h.stopMu.Unlock()
	}
	saveIdentity := func() {
		if p == nil || identitySaved {
			return
		}
		id := p.Identity()
		view, launchViewOK := p.DuplexLaunchState()
		body, _ := json.Marshal(map[string]any{"version": 1, "segment": g.SegmentID, "command": g.CommandID, "pid": id.PID, "pgid": id.PGID, "birth": id.Birth, "birth_known": id.BirthKnown, "actual_executable": id.Executable, "actual_executable_sha256": id.ExecutableSHA256, "duplex_view": launchViewOK, "spawned_at": view.SpawnedAt, "startup_cleanup_present": view.StartupCleanup != nil})
		if codexActorWriteFile(filepath.Join(filepath.Dir(h.spoolRoot), "handle-"+g.SegmentID+".json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Error("original handle identity evidence write failed")
		}
		identitySaved = true
	}
	// Installed before the first goroutine/Start. A handle arriving after an
	// acquisition failure is obtained only from the original CommandID lease.
	// PID searches/reconstructed Handles are never cleanup authority.
	defer func() {
		cancel()
		for !consumed && p == nil && parentCtx.Err() == nil {
			lookup()
			if p != nil {
				break
			}
			select {
			case <-done:
				consumed = true
			case <-parentCtx.Done():
			case <-ticker.C:
			}
		}
		lookup()
		saveIdentity()
		if p != nil && p.ConfirmTreeExited() != nil {
			id := p.Identity()
			// No auxiliary writes after a successful sealed physical exit.
			for _, phase := range []string{"initialize-release", "turn-release", "exit-release"} {
				path := codexActorMarker(home, id.PID, phase)
				if _, err := os.Lstat(path); os.IsNotExist(err) {
					_ = codexActorWriteMarker(home, id.PID, phase)
				}
			}
			if t.Failed() {
				stopCtx, stopCancel := context.WithTimeout(parentCtx, 2*time.Second)
				_ = h.StopSegment(stopCtx, g.SegmentID) // joins original op, never renews it
				stopCancel()
			}
		}
		if !consumed {
			select {
			case <-done:
				consumed = true
			case <-parentCtx.Done():
				t.Error("ExecuteLaunch goroutine remains unjoined")
			}
		}
		waited, absent, pid, pgid, exit := false, false, 0, 0, -1
		if p != nil {
			id := p.Identity()
			pid, pgid, exit = id.PID, id.PGID, p.ExitCode()
			waited, absent = p.Wait(parentCtx) == nil, p.ConfirmTreeExited() == nil
			exit = p.ExitCode()
		}
		cleanup, _ := json.Marshal(map[string]any{"version": 1, "segment": g.SegmentID, "handle_present": p != nil, "pid": pid, "pgid": pgid, "original_wait": waited, "group_absent": absent, "original_exit_code": exit, "execute_joined": consumed})
		if codexActorWriteFile(filepath.Join(filepath.Dir(h.spoolRoot), "cleanup-"+g.SegmentID+".json"), append(cleanup, '\n'), os.O_EXCL) != nil {
			t.Error("original cleanup evidence write failed")
		}
		if !consumed || p != nil && (!waited || !absent) {
			t.Error("original actor cleanup unproved; retain evidence")
		}
	}()
	go func() {
		result, err := h.executeLaunch(ctx, g, inv, codexExecuteControl{actor: actor})
		done <- returned{result, err}
	}()
	for p == nil {
		lookup()
		if p != nil {
			break
		}
		select {
		case value := <-done:
			consumed = true
			t.Fatalf("execution returned before original handle: %s", value.result.Status)
		case <-ctx.Done():
			t.Fatal("original handle acquisition execution deadline")
		case <-ticker.C:
		}
	}
	saveIdentity() // original identity before releasing any child barrier
	id := p.Identity()
	observation := codexActorSegmentEvidence{Segment: g.SegmentID, PID: id.PID, PGID: id.PGID, BirthKnown: id.BirthKnown}
	if codexActorAwaitMarker(ctx, home, id.PID, "initialize-ready") != nil || codexActorWriteMarker(home, id.PID, "initialize-release") != nil {
		t.Fatal("actual child initialize barrier")
	}
	if codexActorAwaitMarker(ctx, home, id.PID, "turn-ready") != nil {
		t.Fatal("actual turn barrier")
	}
	events, err := spool.Read()
	threadInfo, statErr := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, "segment-1", "codex-thread.json"))
	observation.SessionBeforeTurn = err == nil && codexActorHasEvent(events, contract.EventSession) && statErr == nil && threadInfo.Mode().IsRegular() && threadInfo.Mode().Perm() == 0600
	if !observation.SessionBeforeTurn {
		t.Fatal("turn reached before original durable protected binding/Session")
	}
	if codexActorWriteMarker(home, id.PID, "turn-release") != nil {
		t.Fatal("actual child turn release")
	}
	if testCase == "segment_stop" || testCase == "stop_all" {
		stopCtx, stopCancel := context.WithTimeout(ctx, 2*time.Second)
		var stopErr error
		if testCase == "segment_stop" {
			stopErr = h.StopSegment(stopCtx, g.SegmentID)
		} else {
			h.StopAll(stopCtx)
		}
		stopCancel()
		if stopErr != nil {
			t.Fatal("original shared stop failed")
		}
	} else if testCase == "first_canceled_unknown" {
		stopCtx, stopCancel := context.WithCancel(context.Background())
		stopCancel()
		if !errors.Is(h.StopSegment(stopCtx, g.SegmentID), process.ErrProcessTreeUnknown) {
			t.Fatal("first canceled request fabricated new stop window")
		}
	}
	var value returned
	select {
	case value = <-done:
		consumed = true
	case <-ctx.Done():
		t.Fatal("original ExecuteLaunch not joined within execution deadline")
	}
	observation.Status = value.result.Status
	observation.ErrorClass = codexActorErrorClass(value.err)
	if testCase == "first_canceled_unknown" {
		h.stopMu.Lock()
		h.mu.Lock()
		observation.UnknownRetainedBeforeRelease = h.codexLeases[g.CommandID] == lease && lease.handle == p && lease.op != nil && errors.Is(lease.op.err, process.ErrProcessTreeUnknown)
		h.mu.Unlock()
		h.stopMu.Unlock()
		if p.ConfirmTreeExited() == nil || !errors.Is(h.StopSegment(context.Background(), g.SegmentID), process.ErrProcessTreeUnknown) {
			t.Fatal("UNKNOWN follower renewed original failed window")
		}
		if codexActorWriteMarker(home, id.PID, "exit-release") != nil {
			t.Fatal("release retained original UNKNOWN child")
		}
	}
	observation.OriginalWait = p.Wait(parentCtx) == nil
	observation.TreeAbsent = p.ConfirmTreeExited() == nil
	observation.ExitCode = p.ExitCode()
	stderr := p.Output()
	observation.StderrBytes, observation.StderrSHA256 = len(stderr), codexDigest([]byte(stderr))
	events, err = spool.Read()
	if err != nil {
		t.Fatal("durable original spool read")
	}
	for _, event := range events {
		observation.EventKinds = append(observation.EventKinds, event.Kind)
	}
	_, err = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json"))
	observation.SealPresent = err == nil
	if !observation.OriginalWait || !observation.TreeAbsent {
		t.Fatal("original Handle Wait/tree cleanup not proved")
	}
	return events, value.result, value.err, observation
}

// Pure per-call time input; no global clock hook or new lifecycle authority.
func codexActorExecutionDeadline(parent, now time.Time) (time.Time, error) {
	deadline := now.Add(20 * time.Second)
	latestExecution := parent.Add(-11 * time.Second)
	if latestExecution.Before(deadline) {
		deadline = latestExecution
	}
	if !deadline.After(now) {
		return time.Time{}, context.DeadlineExceeded
	}
	return deadline, nil
}
