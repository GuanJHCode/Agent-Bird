//go:build darwin

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const codexNestedOptIn = "root-reviewed-c06-nested-sandbox-v1"
const codexNestedInnerSHA = "9efcfd9acb165fc744bafe95fcbb99a1b21b784a2d7d2e1203d9c981f85a4318"
const codexNestedApplyTemplate = "sandbox-exec: sandbox_apply: Operation not permitted"
const codexNestedNoop = "(version 1)\n(allow default)"

// These observations describe an OS-only diagnostic, never a native turn/seal.
type codexNestedObservation struct {
	Label              string             `json:"label"`
	OriginalHandle     bool               `json:"original_handle"`
	OriginalWait       bool               `json:"original_wait"`
	TreeExited         bool               `json:"tree_exited"`
	TimedOut           bool               `json:"timed_out"`
	ExitCode           int                `json:"exit_code"`
	ExactToken         bool               `json:"exact_token"`
	ExactApplyDenied   bool               `json:"exact_apply_denied"`
	StderrBytes        int                `json:"stderr_bytes"`
	ProtectedUnchanged bool               `json:"protected_unchanged"`
	ExecutablePins     bool               `json:"executable_pins_unchanged"`
	InnerSource        bool               `json:"inner_source_verified"`
	WithinDeadline     bool               `json:"within_original_deadline"`
	PipeJoined         bool               `json:"pipe_joined"`
	Command            codexNestedCommand `json:"command"`
	Identity           process.Identity   `json:"original_identity"`
	Deadline           time.Time          `json:"original_case_deadline"`
	ElapsedMS          int64              `json:"elapsed_ms"`
	StartError         string             `json:"start_error_class"`
	CleanupError       string             `json:"cleanup_error_class"`
	StdoutBytes        int                `json:"stdout_bytes"`
	StdoutSHA256       string             `json:"stdout_sha256"`
	StderrSHA256       string             `json:"stderr_sha256"`
}

type codexNestedCommand struct {
	Path, Dir, ProviderPath, ProviderSHA256, EnvSHA256 string
	Args                                               []string
	ExactEnv                                           bool
}

func codexNestedAccepted(o codexNestedObservation) bool {
	return o.OriginalHandle && o.OriginalWait && o.TreeExited && !o.TimedOut && o.StartError == "none" && o.CleanupError == "none" && o.ExitCode == 0 && o.ExactToken && !o.ExactApplyDenied && o.StderrBytes == 0 && o.ProtectedUnchanged && o.ExecutablePins && o.InnerSource && o.WithinDeadline && o.PipeJoined
}

func codexNestedClassify(observations []codexNestedObservation) string {
	if len(observations) == 4 && observations[0].Label == "N0" && codexNestedAccepted(observations[0]) {
		return "rule_isolation_observations_only"
	}
	if len(observations) == 5 && observations[0].Label == "P1" {
		return "from_noop_observations_only"
	}
	if len(observations) != 3 || observations[0].Label != "A" || observations[1].Label != "B" || observations[2].Label != "C" || !codexNestedAccepted(observations[0]) || !codexNestedAccepted(observations[1]) {
		return "inconclusive"
	}
	c := observations[2]
	if codexNestedAccepted(c) {
		return "static_combo_hypothesis_refuted"
	}
	if c.OriginalHandle && c.OriginalWait && c.TreeExited && !c.TimedOut && c.StartError == "none" && c.CleanupError == "none" && c.PipeJoined && c.ProtectedUnchanged && c.ExecutablePins && c.InnerSource && c.WithinDeadline && c.ExitCode == 71 && c.ExactApplyDenied && c.StdoutBytes == 0 {
		return "nonnoop_inner_apply_refused_under_actual_outer"
	}
	return "inconclusive"
}

func codexNestedRefused(o codexNestedObservation) bool {
	return o.OriginalHandle && o.OriginalWait && o.TreeExited && !o.TimedOut && o.StartError == "none" && o.CleanupError == "none" && o.PipeJoined && o.ProtectedUnchanged && o.ExecutablePins && o.InnerSource && o.WithinDeadline && o.ExitCode == 71 && o.ExactApplyDenied && o.StdoutBytes == 0
}

// Tests-only single-variable variants. They may execute only the fixed cat
// diagnostic below, and are never supplied to a Codex worker or product entry.
func codexNestedOuterVariant(outer, label, input string) (string, string, error) {
	if strings.HasPrefix(label, "P") {
		var added string
		switch label {
		case "P1":
			added = "(deny file-link)"
		case "P2":
			added = "(deny file-write*)"
		case "P3":
			added = "(deny file-read* (literal " + quotedPath(filepath.Join(filepath.Dir(filepath.Dir(input)), "spool", "control-sentinel")) + "))"
		case "P4":
			added = "(deny network*)"
		case "P5":
			added = "(deny file-write* (literal " + quotedPath(filepath.Join(filepath.Dir(input), "never-created-write-target")) + "))"
		default:
			return "", "", errCodexStateFile
		}
		return codexNestedNoop + "\n" + added, added, nil
	}
	if label == "N0" {
		return codexNestedNoop, "noop_control", nil
	}
	lines := strings.Split(outer, "\n")
	index := -1
	for i, line := range lines {
		match := label == "N1" && line == "(deny file-link)" || label == "N2" && line == "(deny file-write*)" || label == "N3" && strings.HasPrefix(line, "(deny file-read* (subpath ")
		if match {
			if index != -1 {
				return "", "", errCodexStateFile
			}
			index = i
		}
	}
	if index == -1 {
		return "", "", errCodexStateFile
	}
	removed := lines[index]
	return strings.Join(append(lines[:index:index], lines[index+1:]...), "\n"), removed, nil
}

// Returns one fixed, independently blob-verified upstream legacy readonly
// composition. This is not a capture of the historical native tool profile.
func codexNestedLoadProfile(source string) (string, error) {
	read := func(name, digest string, size int64) (string, error) {
		path := filepath.Join(source, name)
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() != size {
			return "", errCodexStateFile
		}
		body, err := os.ReadFile(path)
		after, check := os.Lstat(path)
		if err != nil || check != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || codexDigest(body) != digest {
			return "", errCodexStateFile
		}
		return string(body), nil
	}
	base, err := read("codex-rs__sandboxing__src__seatbelt_base_policy.sbpl", "5103332ddb8885ee5e1926de6c0ef23a61f4e55e31297506ae05fa4e0b24ba74", 3910)
	if err != nil {
		return "", err
	}
	prefs, err := read("codex-rs__sandboxing__src__seatbelt_preferences_policy.sbpl", "bfbbe3f051112c7c24dc3a61e09b54cee7e0ea33a420ff7fa9aa4dace03d52aa", 383)
	if err != nil {
		return "", err
	}
	profile := strings.Join([]string{base, "; allow read-only file operations\n(allow file-read*)", "", "", prefs, ""}, "\n")
	if len(profile) != 4350 || codexDigest([]byte(profile)) != codexNestedInnerSHA {
		return "", errCodexStateFile
	}
	return profile, nil
}

func codexNestedTail(cmd process.Command, label, inner, input string) (process.Command, error) {
	if cmd.Path != "/usr/bin/sandbox-exec" || len(cmd.Args) < 5 || cmd.Args[0] != "-p" || cmd.Args[1] == "" || cmd.Args[2] != cmd.PinnedPath || cmd.Args[3] != "app-server" || cmd.Args[4] != "--strict-config" || !codexCanonicalPath(input) {
		return process.Command{}, errCodexStateFile
	}
	args := []string{"-p", inner, "--", "/bin/cat", input}
	switch label {
	case "A", "C":
		if codexDigest([]byte(inner)) != codexNestedInnerSHA {
			return process.Command{}, errCodexStateFile
		}
	case "B", "N0", "N1", "N2", "N3", "P1", "P2", "P3", "P4", "P5":
		if inner != codexNestedNoop {
			return process.Command{}, errCodexStateFile
		}
	default:
		return process.Command{}, errCodexStateFile
	}
	if label != "A" {
		outer := cmd.Args[1]
		if strings.HasPrefix(label, "N") || strings.HasPrefix(label, "P") {
			var err error
			outer, _, err = codexNestedOuterVariant(outer, label, input)
			if err != nil {
				return process.Command{}, err
			}
		}
		args = append([]string{cmd.Args[0], outer, "/usr/bin/sandbox-exec"}, args...)
	}
	cmd.Args = args
	return cmd, nil
}

func codexNestedPins(ctx context.Context, expected map[string]string) bool {
	for path, digest := range expected {
		value, err := process.ExecutableDigest(ctx, path)
		if err != nil || value != digest {
			return false
		}
	}
	return true
}

func codexNestedRunCase(parent context.Context, cmd process.Command, label, profile, input, directory string, token []byte, preserved []string, before map[string]codexSandboxFingerprint, pins map[string]string) codexNestedObservation {
	start := time.Now()
	deadline := start.Add(10 * time.Second)
	parentDeadline, _ := parent.Deadline()
	if deadline.After(parentDeadline) {
		deadline = parentDeadline
	}
	o := codexNestedObservation{Label: label, ExitCode: -1, Deadline: deadline, InnerSource: true, StartError: "none", CleanupError: "none"}
	caseCtx, caseCancel := context.WithDeadline(parent, deadline)
	defer caseCancel()
	work, workCancel := context.WithDeadline(caseCtx, start.Add(5*time.Second))
	defer workCancel()
	var err error
	originalOuter := cmd.Args[1]
	cmd, err = codexNestedTail(cmd, label, profile, input)
	if err != nil {
		o.StartError = "tail_rejected"
		return o
	}
	o.Command = codexNestedCommand{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir, ProviderPath: cmd.PinnedPath, ProviderSHA256: cmd.PinnedSHA256, EnvSHA256: codexDigest([]byte(strings.Join(cmd.Env, "\x00"))), ExactEnv: cmd.ExactEnv}
	// The immutable plan precedes Start; the outer SBPL/env/cwd come directly
	// from the production builder, without a parallel rules implementation.
	body, _ := json.MarshalIndent(o.Command, "", "  ")
	if codexActorWriteFile(filepath.Join(directory, "command.json"), append(body, '\n'), os.O_EXCL) != nil || !codexNestedPins(work, pins) {
		o.StartError = "preflight_rejected"
		return o
	}
	if strings.HasPrefix(label, "N") || strings.HasPrefix(label, "P") {
		variant, removed, err := codexNestedOuterVariant(originalOuter, label, input)
		if err != nil || variant != cmd.Args[1] {
			o.StartError = "variant_rejected"
			return o
		}
		deltaKey := "removed_rule"
		if strings.HasPrefix(label, "P") {
			originalOuter, deltaKey = codexNestedNoop, "added_rule"
		}
		difference, _ := json.MarshalIndent(map[string]string{"original_sha256": codexDigest([]byte(originalOuter)), "variant_sha256": codexDigest([]byte(variant)), deltaKey: removed}, "", "  ")
		if codexActorWriteFile(filepath.Join(directory, "outer-original.sbpl"), []byte(originalOuter), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(directory, "outer-variant.sbpl"), []byte(variant), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(directory, "rule-difference.json"), append(difference, '\n'), os.O_EXCL) != nil {
			o.StartError = "variant_evidence_failed"
			return o
		}
	}
	if label == "P5" {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(input), "never-created-write-target")); !os.IsNotExist(err) {
			o.StartError = "absent_target_preflight"
			return o
		}
	}
	handle, stream, startErr := process.StartDuplex(work, cmd)
	// Retain the original h+err before observing any failure or output.
	o.OriginalHandle = handle != nil
	if startErr != nil {
		o.StartError = codexSandboxErrorClass(startErr)
	}
	var stdout []byte
	type readResult struct {
		body []byte
		err  error
	}
	var read chan readResult
	if stream != nil {
		read = make(chan readResult, 1)
		go func() { body, err := io.ReadAll(io.LimitReader(stream, (64<<10)+1)); read <- readResult{body, err} }()
	}
	if handle != nil {
		o.Identity = handle.Identity()
		if handle.Wait(work) != nil {
			o.TimedOut = true
		}
		if handle.ConfirmTreeExited() != nil {
			if stopErr := handle.Stop(caseCtx); stopErr != nil {
				o.CleanupError = codexSandboxErrorClass(stopErr)
			}
		}
		o.OriginalWait = handle.Wait(caseCtx) == nil
		o.TreeExited = handle.ConfirmTreeExited() == nil
		o.ExitCode = handle.ExitCode()
	}
	if read != nil {
		select {
		case result := <-read:
			stdout, o.PipeJoined = result.body, true
			if result.err != nil {
				o.StartError = "stdout_read"
			}
		case <-work.Done():
			_ = stream.Close()
			select {
			case result := <-read:
				stdout, o.PipeJoined = result.body, true
			case <-caseCtx.Done():
			}
		}
	} else {
		o.PipeJoined = true
	}
	if stream != nil {
		_ = stream.Close()
	}
	stderr := []byte{}
	if handle != nil {
		stderr = []byte(handle.Output())
	}
	o.StdoutBytes, o.StderrBytes = len(stdout), len(stderr)
	o.StdoutSHA256, o.StderrSHA256 = codexDigest(stdout), codexDigest(stderr)
	o.ExactToken = bytes.Equal(stdout, token)
	o.ExactApplyDenied = strings.TrimSpace(string(stderr)) == codexNestedApplyTemplate
	// Persist only controlled synthetic bytes and the fixed public template.
	if len(stdout) == 0 || o.ExactToken {
		_ = codexActorWriteFile(filepath.Join(directory, "stdout"), stdout, os.O_EXCL)
	}
	if len(stderr) == 0 || o.ExactApplyDenied {
		_ = codexActorWriteFile(filepath.Join(directory, "stderr"), stderr, os.O_EXCL)
	}
	after, snapshotErr := codexSandboxSnapshot(preserved)
	o.ProtectedUnchanged = snapshotErr == nil && reflect.DeepEqual(before, after)
	if label == "P5" {
		_, err := os.Lstat(filepath.Join(filepath.Dir(input), "never-created-write-target"))
		o.ProtectedUnchanged = o.ProtectedUnchanged && os.IsNotExist(err)
	}
	o.ExecutablePins = codexNestedPins(caseCtx, pins)
	o.WithinDeadline = caseCtx.Err() == nil && time.Now().Before(deadline)
	o.ElapsedMS = time.Since(start).Milliseconds()
	if len(stdout) > 64<<10 || len(stderr) > 64<<10 {
		o.StartError = "output_limit"
		o.ExactToken = false
	}
	return o
}

// Explicit double opt-in. Default test runs do not execute OS helpers.
func TestCodexNestedSandboxMatrixActor(t *testing.T) {
	codexNestedSandboxMatrix(t, "readonly")
}

func TestCodexNestedRuleIsolationMatrixActor(t *testing.T) {
	codexNestedSandboxMatrix(t, "remove_rules")
}

func TestCodexNestedFromNoopMatrixActor(t *testing.T) {
	codexNestedSandboxMatrix(t, "add_from_noop")
}

func codexNestedSandboxMatrix(t *testing.T, mode string) {
	ruleIsolation := mode != "readonly"
	if os.Getenv("C06_NESTED_OPT_IN") != codexNestedOptIn || os.Getenv("C06_NESTED_EVIDENCE_DIR") == "" {
		t.Skip("explicit ROOT-owned no-model matrix only")
	}
	evidence := os.Getenv("C06_NESTED_EVIDENCE_DIR")
	anchor, err := codexAnchorStateRoot(context.Background(), evidence)
	if err != nil || len(anchor.Parents) == 0 || anchor.Parents[len(anchor.Parents)-1].Mode != 0700 {
		t.Fatal("canonical owner-only evidence required")
	}
	base := filepath.Join(evidence, "matrix")
	if os.Mkdir(base, 0700) != nil {
		t.Fatal("fresh no-reuse matrix required")
	}
	parent, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var observations []codexNestedObservation
	var before, after map[string]codexSandboxFingerprint
	var pins map[string]string
	phase := "prepare"
	defer func() {
		report := map[string]any{"version": 1, "phase": phase, "classification": codexNestedClassify(observations), "failed": t.Failed(), "cases": observations, "snapshot_before": before, "snapshot_after": after, "inner_sha256": codexNestedInnerSHA, "inner_source_scope": "fixed_upstream_legacy_readonly_composition_not_historical_profile_capture", "native_model_calls": 0, "native_turns": 0, "provider_executed": false, "public_entry": false, "rule_isolation": ruleIsolation, "executable_pins": pins}
		if ruleIsolation {
			report["inner_sha256"], report["inner_source_scope"] = codexDigest([]byte(codexNestedNoop)), "fixed_noop_rule_isolation_control"
		}
		if mode == "add_from_noop" {
			report["reused_positive_control"] = map[string]string{"source": "data/rule-isolation-once/matrix/report.json#N0", "report_sha256": "d228e739996dacbe0e59e7a6d8402bf97b484a7e2b08b396c4a0a5e0b947f440", "scope": "same_explicit_synthetic_environment_shape_and_original_launcher_with_new_owned_fixture_paths"}
		}
		body, _ := json.MarshalIndent(report, "", "  ")
		if codexActorWriteFile(filepath.Join(base, "report.json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Error("durable matrix report failed")
		}
	}()
	profile, err := codexNestedLoadProfile(os.Getenv("C06_NESTED_UPSTREAM_DIR"))
	if err != nil {
		t.Fatal("fixed upstream profile rejected")
	}
	pin := os.Getenv("C06_NESTED_PROVIDER_PATH")
	pins = map[string]string{}
	for _, path := range []string{pin, "/usr/bin/sandbox-exec", "/bin/cat"} {
		digest, err := process.ExecutableDigest(parent, path)
		if err != nil {
			t.Fatal("canonical executable pin rejected")
		}
		pins[path] = digest
	}
	if pins[pin] != adapter.CodexSHA256 {
		t.Fatal("fixed native provider identity rejected")
	}
	source, workspace, spool := filepath.Join(base, "source"), filepath.Join(base, "workspace"), filepath.Join(base, "spool")
	for _, path := range []string{source, workspace, spool} {
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("fresh owned fixture failed")
		}
	}
	if codexActorWriteFile(filepath.Join(source, "config.toml"), []byte("model=\"gpt-fixed\"\n"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(source, "auth.json"), []byte("SYNTHETIC_AUTH_ONLY\n"), os.O_EXCL) != nil || codexActorWriteFile(filepath.Join(spool, "control-sentinel"), []byte("SYNTHETIC_CONTROL\n"), os.O_EXCL) != nil {
		t.Fatal("synthetic source/control setup failed")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal("synthetic token entropy failed")
	}
	token := []byte("C06_SYNTHETIC_" + hex.EncodeToString(random) + "\n")
	input := filepath.Join(workspace, "readonly-input")
	if codexActorWriteFile(input, token, os.O_EXCL) != nil {
		t.Fatal("synthetic workspace input failed")
	}
	h, err := NewIPC(spool, "nested-producer")
	if err != nil || h.BindContinuationOwner("nested-owner") != nil {
		t.Fatal("owned Host construction failed")
	}
	profileView := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 60000}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: adapter.BinaryPin{Path: pin, SHA256: adapter.CodexSHA256, Version: adapter.CodexVersion}}
	payload, _ := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: workspace, Prompt: "synthetic sandbox diagnostic", Profile: &profileView, ProviderLock: &lock})
	g := contract.LaunchCommand{RunID: "nested-run", TaskID: "nested-task", AttemptID: "nested-attempt", SegmentID: "segment-1", WorkRevision: 1, PlanRevision: 1, CommandID: "command-1", HostID: "nested-host", ExecutionEpoch: 1, AdapterPayload: payload}
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: lock.Binary, Lock: &lock, Profile: &profileView, CWD: workspace, Prompt: "synthetic sandbox diagnostic"})
	if err != nil {
		t.Fatal("managed descriptor rejected")
	}
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal("owned spool failed")
	}
	env := []string{"HOME=" + source, "CODEX_HOME=" + source, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	prepared, err := h.prepareManagedCodexState(parent, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal("actual state constructor failed")
	}
	cmd, prepared, err := codexManagedCommand(parent, prepared, env, spool)
	if err != nil {
		t.Fatal("actual command builder failed")
	}
	preserved := []string{source, filepath.Join(source, "config.toml"), filepath.Join(source, "auth.json"), workspace, input, spool, filepath.Join(spool, "control-sentinel"), prepared.Protected.Path, filepath.Join(prepared.Protected.Path, "codex-origin.json"), prepared.Layout.State.Path, prepared.Layout.Home.Path, filepath.Join(prepared.Layout.Home.Path, "config.toml"), filepath.Join(prepared.Layout.Home.Path, "auth.json")}
	before, err = codexSandboxSnapshot(preserved)
	if err != nil {
		t.Fatal("original synthetic snapshot failed")
	}
	labels := []string{"A", "B", "C"}
	if ruleIsolation {
		labels = []string{"N0", "N1", "N2", "N3"}
	}
	if mode == "add_from_noop" {
		labels = []string{"P1", "P2", "P3", "P4", "P5"}
	}
	for _, label := range labels {
		phase = label
		directory := filepath.Join(base, label)
		if os.Mkdir(directory, 0700) != nil {
			t.Fatal("fresh case evidence failed")
		}
		inner := profile
		if label == "B" || ruleIsolation {
			inner = codexNestedNoop
		}
		observation := codexNestedRunCase(parent, cmd, label, inner, input, directory, token, preserved, before, pins)
		observations = append(observations, observation)
		after, err = codexSandboxSnapshot(preserved)
		body, _ := json.MarshalIndent(observation, "", "  ")
		if codexActorWriteFile(filepath.Join(directory, "observation.json"), append(body, '\n'), os.O_EXCL) != nil {
			t.Fatal("original case evidence failed")
		}
		known := codexNestedAccepted(observation)
		if ruleIsolation && label != "N0" {
			known = known || codexNestedRefused(observation)
		}
		if err != nil || !known {
			t.Fatalf("case %s failed: exit=%d apply_denied=%t wait=%t tree=%t preserved=%t", label, observation.ExitCode, observation.ExactApplyDenied, observation.OriginalWait, observation.TreeExited, observation.ProtectedUnchanged)
		}
	}
	phase = "complete"
}

func TestCodexNestedTailPreservesActualBuilderScope(t *testing.T) {
	h, g, inv, env := codexOriginFixture(t)
	p, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, err := codexManagedCommand(context.Background(), p, env, h.spoolRoot)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]string(nil), cmd.Args...)
	input := filepath.Join(inv.WorkingDirectory(), "synthetic-input")
	out, err := codexNestedTail(cmd, "B", codexNestedNoop, input)
	if err != nil || !reflect.DeepEqual(cmd.Args, original) || out.Args[1] != cmd.Args[1] || out.Dir != cmd.Dir || !reflect.DeepEqual(out.Env, cmd.Env) || out.PinnedPath != cmd.PinnedPath || out.PinnedSHA256 != cmd.PinnedSHA256 || !out.ExactEnv {
		t.Fatal("test substitution changed original scope or command")
	}
	for _, label := range []string{"unknown", "A", "C"} {
		if _, err := codexNestedTail(cmd, label, codexNestedNoop, input); !errors.Is(err, errCodexStateFile) {
			t.Fatal("unknown or unpinned inner accepted")
		}
	}
	for _, label := range []string{"N0", "N1", "N2", "N3"} {
		out, err := codexNestedTail(cmd, label, codexNestedNoop, input)
		variant, _, variantErr := codexNestedOuterVariant(cmd.Args[1], label, input)
		if err != nil || variantErr != nil || out.Args[1] != variant || !reflect.DeepEqual(cmd.Args, original) || !reflect.DeepEqual(out.Env, cmd.Env) || out.Dir != cmd.Dir || out.PinnedPath != cmd.PinnedPath || out.PinnedSHA256 != cmd.PinnedSHA256 {
			t.Fatal("rule variant changed an unrelated command boundary")
		}
	}
}

// Dropping an original-Wait, source, output or protection requirement must
// not turn a missing/failed launch into a successful read diagnostic.
func TestCodexNestedAcceptanceRejectsUnprovedRead(t *testing.T) {
	good := codexNestedObservation{Label: "A", OriginalHandle: true, OriginalWait: true, TreeExited: true, ExitCode: 0, ExactToken: true, ProtectedUnchanged: true, ExecutablePins: true, InnerSource: true, WithinDeadline: true, PipeJoined: true, StartError: "none", CleanupError: "none"}
	if !codexNestedAccepted(good) {
		t.Fatal("complete synthetic read observation rejected")
	}
	for _, field := range []string{"handle", "wait", "tree", "timeout", "exit", "token", "denial", "stderr", "protected", "pin", "source", "deadline", "pipe"} {
		t.Run(field, func(t *testing.T) {
			bad := good
			switch field {
			case "handle":
				bad.OriginalHandle = false
			case "wait":
				bad.OriginalWait = false
			case "tree":
				bad.TreeExited = false
			case "timeout":
				bad.TimedOut = true
			case "exit":
				bad.ExitCode = 71
			case "token":
				bad.ExactToken = false
			case "denial":
				bad.ExactApplyDenied = true
			case "stderr":
				bad.StderrBytes = 1
			case "protected":
				bad.ProtectedUnchanged = false
			case "pin":
				bad.ExecutablePins = false
			case "source":
				bad.InnerSource = false
			case "deadline":
				bad.WithinDeadline = false
			case "pipe":
				bad.PipeJoined = false
			}
			if codexNestedAccepted(bad) {
				t.Fatal("incomplete/failed observation accepted")
			}
		})
	}
}
