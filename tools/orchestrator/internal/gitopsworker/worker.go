package gitopsworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
)

type requestEnvelope struct {
	Version     int             `json:"version"`
	Kind        string          `json:"kind"`
	Operation   string          `json:"operation"`
	Request     json.RawMessage `json:"request"`
	JournalRoot string          `json:"journal_root"`
}

type Invocation struct {
	args []string
	dir  string
}

func (i Invocation) Args() []string                 { return append([]string(nil), i.args...) }
func (i Invocation) WorkingDirectory() string       { return i.dir }
func (i Invocation) Stdin() []byte                  { return nil }
func (i Invocation) Environment() map[string]string { return map[string]string{} }

// BuildInvocation durably materializes a source-owned worker request and
// returns an invocation of the same pinned orchestrator binary. Raw Git input
// is never placed on argv or sent through the coordinator as environment.
func BuildInvocation(payload json.RawMessage, executable, spoolRoot, commandID string) (Invocation, error) {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(spoolRoot) || commandID == "" {
		return Invocation{}, errors.New("gitops_worker_input_invalid")
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Invocation{}, errors.New("gitops_worker_executable_unsafe")
	}
	var supplied struct {
		Kind      string          `json:"kind"`
		Operation string          `json:"operation"`
		Request   json.RawMessage `json:"request"`
	}
	if err = decode(payload, &supplied); err != nil || supplied.Kind != "gitops" || !validOperation(supplied.Operation) || len(supplied.Request) == 0 || !json.Valid(supplied.Request) {
		return Invocation{}, errors.New("gitops_worker_request_invalid")
	}
	workingDirectory, err := requestWorkingDirectory(supplied.Operation, supplied.Request)
	if err != nil {
		return Invocation{}, err
	}
	requestDir := filepath.Join(spoolRoot, "gitops-requests")
	journalRoot := filepath.Join(spoolRoot, "gitops-journal", digest(commandID))
	if err = ensurePrivateDir(requestDir); err != nil {
		return Invocation{}, err
	}
	if err = ensurePrivateDir(journalRoot); err != nil {
		return Invocation{}, err
	}
	envelope := requestEnvelope{Version: 1, Kind: "gitops", Operation: supplied.Operation, Request: supplied.Request, JournalRoot: journalRoot}
	body, err := json.Marshal(envelope)
	if err != nil {
		return Invocation{}, err
	}
	body = append(body, '\n')
	path := filepath.Join(requestDir, digest(commandID)+".json")
	if err = writeExclusiveOrSame(path, body); err != nil {
		return Invocation{}, err
	}
	return Invocation{args: []string{executable, "gitops-worker", "--request", path}, dir: workingDirectory}, nil
}

// Run executes one already-materialized worker request. The caller supplies
// stdout so cmd/orchestrator can expose a thin internal subcommand without
// importing Git behavior into the coordinator process.
func Run(ctx context.Context, requestPath string, stdout io.Writer) error {
	if stdout == nil {
		return errors.New("gitops_worker_stdout_required")
	}
	var envelope requestEnvelope
	if err := readPrivateJSON(requestPath, &envelope); err != nil {
		return err
	}
	if envelope.Version != 1 || envelope.Kind != "gitops" || !validOperation(envelope.Operation) || !filepath.IsAbs(envelope.JournalRoot) {
		return errors.New("gitops_worker_request_invalid")
	}
	journal, err := openJournal(envelope.JournalRoot)
	if err != nil {
		return err
	}
	defer journal.close()

	var result any
	switch envelope.Operation {
	case "compose":
		var request gitops.CandidateCompositionRequest
		if err = decode(envelope.Request, &request); err == nil {
			result, err = gitops.ComposeCandidate(ctx, request, journal)
		}
	case "create_private_ref":
		var request gitops.PrivateRefRequest
		if err = decode(envelope.Request, &request); err == nil {
			result, err = gitops.CreatePrivateRef(ctx, request, journal)
		}
	case "delete_private_ref":
		var request gitops.DeletePrivateRefRequest
		if err = decode(envelope.Request, &request); err == nil {
			result, err = gitops.DeletePrivateRefWithReceipt(ctx, request, journal)
		}
	case "materialize":
		var request gitops.MaterializeRequest
		if err = decode(envelope.Request, &request); err == nil {
			result, err = gitops.Materialize(ctx, request, journal)
		}
	case "integrate":
		var request gitops.IntegrationRequest
		if err = decode(envelope.Request, &request); err == nil {
			request.Journal = journal
			result, err = gitops.Integrate(ctx, request)
		}
	case "cleanup":
		var request gitops.CleanupRequest
		if err = decode(envelope.Request, &request); err == nil {
			request.Journal = journal
			result, err = gitops.Cleanup(ctx, request)
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "ok", "operation": envelope.Operation, "result": result})
}

type durableJournal struct {
	file *os.File
}

func openJournal(root string) (*durableJournal, error) {
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, errors.New("gitops_journal_unsafe")
	}
	directory, err := os.Open(root)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &durableJournal{file: file}, nil
}

func (j *durableJournal) close() { _ = j.file.Close() }
func (j *durableJournal) append(kind string, value any) error {
	body, err := json.Marshal(map[string]any{"version": 1, "kind": kind, "value": value})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if _, err = j.file.Write(body); err != nil {
		return err
	}
	return j.file.Sync()
}
func (j *durableJournal) PersistIntegrationIntent(_ context.Context, value gitops.IntegrationIntent) error {
	return j.append("integration_intent", value)
}
func (j *durableJournal) RecordIntegration(_ context.Context, value gitops.IntegrationOutcome) error {
	return j.append("integration_outcome", value)
}
func (j *durableJournal) PersistCleanupIntent(_ context.Context, value gitops.CleanupIntent) error {
	return j.append("cleanup_intent", value)
}
func (j *durableJournal) RecordCleanup(_ context.Context, value gitops.CleanupOutcome) error {
	return j.append("cleanup_outcome", value)
}
func (j *durableJournal) PersistCandidateIntent(_ context.Context, value gitops.CandidateIntent) error {
	return j.append("candidate_intent", value)
}
func (j *durableJournal) PersistCandidateRefIntent(_ context.Context, value gitops.CandidateRefIntent) error {
	return j.append("candidate_ref_intent", value)
}
func (j *durableJournal) RecordCandidateFreeze(_ context.Context, value gitops.CandidateReceipt) error {
	return j.append("candidate_receipt", value)
}
func (j *durableJournal) PersistPrivateRefIntent(_ context.Context, value gitops.PrivateRefIntent) error {
	return j.append("private_ref_intent", value)
}
func (j *durableJournal) RecordPrivateRef(_ context.Context, value gitops.PrivateRefOutcome) error {
	return j.append("private_ref_outcome", value)
}

func validOperation(value string) bool {
	return value == "compose" || value == "create_private_ref" || value == "delete_private_ref" || value == "materialize" || value == "integrate" || value == "cleanup"
}

func requestWorkingDirectory(operation string, body json.RawMessage) (string, error) {
	var value struct {
		RepoRoot string `json:"repo_root"`
		Root     string `json:"root"`
		Receipt  struct {
			RepoRoot string `json:"repo_root"`
		} `json:"receipt"`
		CandidateReceipt struct {
			RepoRoot string `json:"repo_root"`
		} `json:"candidate_receipt"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return "", errors.New("gitops_worker_request_invalid")
	}
	directory := value.RepoRoot
	if operation == "cleanup" {
		directory = value.Receipt.RepoRoot
		if directory == "" {
			directory = value.CandidateReceipt.RepoRoot
		}
		if directory == "" {
			directory = filepath.Dir(value.Root)
		}
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("gitops_worker_workdir_invalid")
	}
	return filepath.Clean(directory), nil
}

func ensurePrivateDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("gitops_private_path_invalid")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !owned || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("gitops_private_dir_unsafe")
	}
	return nil
}

func readPrivateJSON(path string, target any) error {
	if !filepath.IsAbs(path) {
		return errors.New("gitops_request_path_invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("gitops_request_unsafe")
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("gitops_request_unsafe")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decode(body, target)
}

func writeExclusiveOrSame(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, body) {
			return nil
		}
		return errors.New("gitops_request_conflict")
	}
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func decode(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("gitops_json_trailing_data")
	}
	return nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var _ gitops.Journal = (*durableJournal)(nil)
var _ gitops.CandidateJournal = (*durableJournal)(nil)
var _ gitops.PrivateRefJournal = (*durableJournal)(nil)
