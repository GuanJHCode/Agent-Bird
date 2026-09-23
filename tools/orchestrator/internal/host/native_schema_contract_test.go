package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const nativeReviewSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["approve","reject"]},"summary":{"type":"string","minLength":1,"maxLength":65536}},"required":["decision","summary"],"additionalProperties":false}`

// TestNativeSchemaContract is deliberately opt-in. It makes exactly one
// provider request, under the production Host sandbox and pinned executable,
// and saves bounded raw stdout for a human to establish the native envelope.
// It neither selects credentials nor changes provider configuration.
func TestNativeSchemaContract(t *testing.T) {
	provider := adapter.Provider(os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_PROVIDER"))
	if provider != adapter.ProviderAGY && provider != adapter.ProviderGrok {
		t.Skip("set AGENT_BIRD_NATIVE_SCHEMA_PROVIDER to antigravity-cli or grok-build")
	}
	evidence := os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_EVIDENCE_DIR")
	if evidence == "" || !filepath.IsAbs(evidence) {
		t.Fatal("AGENT_BIRD_NATIVE_SCHEMA_EVIDENCE_DIR must be an absolute private directory")
	}
	evidence, err := filepath.EvalSymlinks(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(evidence); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("evidence directory is not private: %v %v", info, err)
	}

	pinFile := os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_PIN")
	info, err := os.Lstat(pinFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("private native pin file required")
	}
	rawPin, err := os.ReadFile(pinFile)
	if err != nil {
		t.Fatal(err)
	}
	var pin adapter.BinaryPin
	if json.Unmarshal(rawPin, &pin) != nil {
		t.Fatal("invalid native pin")
	}
	if err := adapter.VerifyExecutable(pin, pin.Version); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	help, err := adapter.ProbeOutput(ctx, pin.Path, "--help")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(evidence, "work-"+string(provider))
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "calc.py"), []byte("def add(a, b):\n    return a - b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 120000, GrokSessionWrite: provider == adapter.ProviderGrok}
	lock := &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}
	req := adapter.Request{Provider: provider, Binary: pin, Lock: lock, CWD: work, Prompt: fmt.Sprintf("Review ONLY this exact file: %s. Requirement: add(2, 3) must return 5. Read that absolute path using read_file (Grok) or view_file (AGY); do not search home or other directories. Use only read operations, do not invoke other agents, and return the schema object with your approve/reject decision and a concise reason. In summary cite the exact return statement from the file and the actual and expected values for add(2, 3).", filepath.Join(work, "calc.py")), Profile: profile}
	var snapshot []byte
	if provider == adapter.ProviderGrok {
		snapshot = nativeReviewSnapshot(t, ctx, work)
		req.Prompt = "Requirement: calc.py add(2, 3) must return 5. Review the actual candidate content in the supplied snapshot. In summary cite the exact return statement and the actual and expected values."
	}

	req.Action = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "native-schema-contract"}
	if err = adapter.CheckCapabilities(req, string(help)); err != nil {
		t.Fatal(err)
	}

	inv, err := adapter.BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := inv.Args()
	schemaCount := 0
	for i, arg := range args {
		if arg == "--json-schema" {
			schemaCount++
			if i+1 >= len(args) || args[i+1] != nativeReviewSchema {
				t.Fatal("candidate review schema changed")
			}
		}
	}
	if schemaCount != 1 {
		t.Fatal("candidate review schema missing or duplicated")
	}
	cmd := process.Command{Path: args[0], Args: args[1:], Dir: inv.WorkingDirectory(), Stdin: inv.Stdin(), PinnedPath: pin.Path, PinnedSHA256: pin.SHA256}
	for key, value := range inv.Environment() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	scratch := filepath.Join(evidence, "scratch-"+string(provider))
	inputDigest, snapshotDigest := "", ""
	nativeGrant := contract.LaunchCommand{CommandID: "native-schema-contract-" + string(provider), RunID: "native-schema-" + hashBytes([]byte(evidence))[:16], TaskID: "review"}
	if provider == adapter.ProviderGrok {
		cmd, inputDigest, snapshotDigest, err = prepareGrokSnapshotPrompt(cmd, snapshot, hashBytes([]byte("native-fixture-validation")), scratch)
		if err == nil {
			cmd, err = prepareAuthenticatedGrokCommand(ctx, cmd, pin, profile, nativeGrant, scratch)
		}

	} else {
		cmd, err = sandboxCommand(ctx, cmd, profile, scratch)
	}
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(evidence, "native-schema-"+string(provider)+".jsonl")
	stdout, err := os.OpenFile(fixture, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	sink := &nativeEvidenceSink{writer: stdout, remaining: 1024 * 1024, cancel: cancel}
	cmd.Stdout = sink
	intent := filepath.Join(evidence, "launch-intent.json")
	intentFile, err := os.OpenFile(intent, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := json.NewEncoder(intentFile).Encode(map[string]any{"provider": provider, "max_active_ms": 120000, "max_attempts": 1})
	if err := errors.Join(encodeErr, intentFile.Sync(), intentFile.Close()); err != nil {
		t.Fatal(err)
	}
	p, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	waitCtx, waitCancel := context.WithDeadline(ctx, deadline.Add(-5*time.Second))
	waitErr := p.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		stopCtx, stopCancel := context.WithDeadline(context.Background(), deadline)
		stopErr := p.Stop(stopCtx)
		stopCancel()
		if stopErr != nil {
			t.Errorf("native process stop: %v", stopErr)
		}
	}
	treeErr := p.ConfirmTreeExited()
	_ = stdout.Sync()
	_ = stdout.Close()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "combined-output.txt"), []byte(p.Output()), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"exit_code": p.ExitCode(), "stdout_bytes": len(raw), "wait_error": fmtError(waitErr), "tree_error": fmtError(treeErr), "sink_error": fmtError(sink.Err()), "review_input_sha256": inputDigest, "review_snapshot_sha256": snapshotDigest})
	if err := os.WriteFile(filepath.Join(evidence, "result.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if sink.Err() != nil || waitErr != nil || treeErr != nil || len(raw) == 0 || len(raw) > 1024*1024 || p.ExitCode() != 0 {
		t.Fatalf("native execution unsuccessful; retained evidence: wait=%v tree=%v exit=%d", waitErr, treeErr, p.ExitCode())
	}
	source, err := os.ReadFile(filepath.Join(work, "calc.py"))
	if err != nil || string(source) != "def add(a, b):\n    return a - b\n" {
		t.Fatal("readonly source changed")
	}
	if provider != adapter.ProviderAGY {
		if err := verifyGrokReviewInput(scratch, inputDigest); err != nil {
			t.Fatal(err)
		}
		collector, _ := newProtocolCollector(string(provider))
		collector.expectedSessionID = grokSessionID(nativeGrant)
		_, _ = collector.Write(raw)
		terminal, _, e := collector.Finish()
		if e != nil || !providerSucceeded(provider, terminal) {
			t.Fatalf("Grok terminal: %v %+v", e, terminal)
		}
		decision, e := decodeCandidateReview(terminal.StructuredText)
		if e != nil || decision.Decision != "reject" {
			t.Fatalf("Grok review invalid: %v", e)
		}
		for _, finding := range []string{"return a - b", "-1", "5"} {
			if !strings.Contains(decision.Summary, finding) {
				t.Fatal("Grok did not identify actual snapshot defect")
			}
		}
		if inputDigest == "" || snapshotDigest == "" {
			t.Fatal("missing source binding")
		}
		return
	}
	if err := validateNativeAGYReview(raw, filepath.Join(work, "calc.py")); err != nil {
		t.Fatal(err)
	}

}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The file sink must enforce its own bound; process.Output only bounds memory.
type nativeEvidenceSink struct {
	mu        sync.Mutex
	writer    io.Writer
	remaining int
	cancel    context.CancelFunc
	err       error
}

func (s *nativeEvidenceSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	q := p
	if len(q) > s.remaining {
		q = q[:s.remaining]
	}
	n, err := s.writer.Write(q)
	s.remaining -= n
	if err == nil && n != len(q) {
		err = io.ErrShortWrite
	}
	if err == nil && len(p) > len(q) {
		err = errors.New("native_evidence_limit")
	}
	if err != nil {
		s.err = err
		s.cancel()
	}
	return n, err
}
func (s *nativeEvidenceSink) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func validateNativeAGYReview(raw []byte, fixture string) error {
	collector, err := newProtocolCollector(string(adapter.ProviderAGY))
	if err != nil {
		return err
	}
	if _, err = collector.Write(raw); err != nil {
		return err
	}
	terminal, session, err := collector.Finish()
	if err != nil {
		return err
	}
	if !providerSucceeded(adapter.ProviderAGY, terminal) {
		return errors.New("native_review_unsuccessful")
	}
	// Require a completed native file-read event for the exact fixture in this
	// terminal's conversation. Model prose alone cannot establish that it read it.
	read := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var envelope struct {
			Event string `json:"event"`
			Step  struct {
				Session string `json:"conversation_id"`
				State   string `json:"state"`
				Type    string `json:"step_type"`
				Tool    string `json:"tool_name"`
				Info    struct {
					Name       string `json:"name"`
					Parameters struct {
						Path string `json:"AbsolutePath"`
					} `json:"parameters"`
					Error json.RawMessage `json:"error"`
				} `json:"tool_info"`
			} `json:"step_update"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return err
		}
		step := envelope.Step
		if envelope.Event == "step_update" && step.Session == session && step.Type == "tool" && step.State == "DONE" && step.Tool == "view_file" && step.Info.Name == "view_file" && step.Info.Parameters.Path == fixture && (len(step.Info.Error) == 0 || string(step.Info.Error) == "null") {
			read = true
		}
	}
	if !read {
		return errors.New("native_review_file_read_missing")
	}
	decision, err := decodeCandidateReview(terminal.StructuredText)
	if err != nil {
		return err
	}
	if decision.Decision != "reject" {
		return errors.New("native_review_wrong_decision")
	}
	// A rejection caused by a missing file is not a successful bug review.
	// These fixture-specific findings do not change the production schema.
	for _, evidence := range []string{"return a - b", "-1", "5"} {
		if !strings.Contains(decision.Summary, evidence) {
			return errors.New("native_review_finding_missing")
		}
	}
	return nil
}
func TestNativeEvidenceSinkBound(t *testing.T) {
	var b bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &nativeEvidenceSink{writer: &b, remaining: 4, cancel: cancel}
	if n, err := s.Write([]byte("123")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := s.Write([]byte("456")); n != 1 || err == nil {
		t.Fatal(n, err)
	}
	if b.String() != "1234" || ctx.Err() == nil || s.Err() == nil {
		t.Fatal("unbounded or uncancelled sink")
	}
	if n, err := s.Write([]byte("7")); n != 0 || err == nil || b.Len() != 4 {
		t.Fatal(n, err)
	}
}

type nativeFailedWriter struct{}

func (nativeFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestNativeEvidenceSinkWriteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &nativeEvidenceSink{writer: nativeFailedWriter{}, remaining: 4, cancel: cancel}
	if _, err := s.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) || ctx.Err() == nil {
		t.Fatal(err)
	}
}
func TestNativeAGYReviewValidation(t *testing.T) {
	terminal := `{"event":"result","result":{"conversation_id":"s1","status":"SUCCESS","response":"done","structured_output":{"decision":"reject","summary":"calc.py: return a - b yields -1; expected 5"}}}` + "\n"
	read := `{"event":"step_update","step_update":{"conversation_id":"s1","state":"DONE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/private/fixture/calc.py"}}}}` + "\n"
	good := read + terminal
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"success", good, true},
		{"no-native-read", terminal, false},
		{"guessed-finding-without-read", strings.Replace(terminal, "calc.py: return a - b yields -1; expected 5", "could not locate calc.py; guessed return a - b yields -1; expected 5", 1), false},
		{"wrong-read-path", strings.Replace(good, "/private/fixture/calc.py", "/private/elsewhere/calc.py", 1), false},
		{"failed-read", strings.Replace(good, `"state":"DONE"`, `"state":"ERROR"`, 1), false},
		{"file-not-found-is-not-business-acceptance", strings.Replace(good, "calc.py: return a - b yields -1; expected 5", "could not locate calc.py", 1), false},
		{"error", strings.Replace(good, "SUCCESS", "ERROR", 1), false},
		{"duplicate", good + good, false},
		{"changed-session", `{"event":"init","conversation_id":"s2"}` + "\n" + good, false},
		{"missing-session", strings.Replace(good, `"s1"`, `""`, 1), false},
		{"missing-terminal", `{"event":"init","conversation_id":"s1"}` + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateNativeAGYReview([]byte(tc.raw), "/private/fixture/calc.py"); (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}

// Only used in this authorized native fixture; production uses the accepted
// Host receipt after managed materialization, never a caller's source bundle.
func nativeReviewSnapshot(t *testing.T, ctx context.Context, work string) []byte {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", work}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Native Review Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("commit", "--allow-empty", "-qm", "base")
	base := git("rev-parse", "HEAD")
	git("add", "calc.py")
	git("commit", "-qm", "candidate")
	receipt := gitops.CandidateReceipt{RepoRoot: work, Worktree: work, CommonDir: filepath.Join(work, ".git"), BaseOID: base, CandidateOID: git("rev-parse", "HEAD"), TreeOID: git("rev-parse", "HEAD^{tree}"), Changes: []gitops.CandidateChange{{Path: "calc.py", Mode: "100644", BlobOID: git("rev-parse", "HEAD:calc.py")}}}
	body, err := gitops.CandidateReviewSnapshot(ctx, receipt)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
