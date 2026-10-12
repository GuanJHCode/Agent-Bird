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
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
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
			if err = syscall.Flock(int(journal.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				// Another worker may already own side effects for this command.
				// Failure to acquire its journal cannot prove a definite failure.
				err = errors.Join(process.ErrProcessTreeUnknown, err)
			} else {
				err = journal.composePreflight(ctx)
			}
			if err == nil {
				result, err = gitops.ManagedComposeCandidate(gitops.WithCandidateJournal(ctx, journal), request, journal)
			}
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
			result, err = gitops.ManagedMaterialize(gitops.WithCandidateJournal(ctx, journal), request, journal)
		}
	case "integrate":
		var request gitops.IntegrationRequest
		if err = decode(envelope.Request, &request); err == nil {
			if err = syscall.Flock(int(journal.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				break
			}
			// Held until journal.close: serialize observation with every Git side effect.
			if err = journal.integrationPreflight(ctx); err != nil {
				break
			}
			request.Journal = journal
			result, err = gitops.TrackedIntegrate(gitops.WithCandidateJournal(ctx, journal), request)
		}
	case "cleanup":
		var request gitops.CleanupRequest
		if err = decode(envelope.Request, &request); err == nil {
			request.Journal = journal
			result, err = gitops.Cleanup(ctx, request)
		}
	}
	if err != nil {
		if envelope.Operation == "compose" {
			// Compose is the only operation whose caller needs a durable, safe
			// classification after a non-zero worker exit. Never serialize an
			// arbitrary error (which may contain a repository path or Git output).
			_ = json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "error", "operation": "compose", "code": composeErrorCode(err)})
		}
		return err
	}
	if envelope.Operation == "compose" {
		// The journal owns the complete inventory for safe cleanup. The public
		// receipt is deliberately bounded for the Host terminal artifact.
		if receipt, ok := result.(gitops.CandidateReceipt); ok {
			receipt.Files = nil
			result = receipt
		}
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"version": 1, "status": "ok", "operation": envelope.Operation, "result": result})
}

func composeErrorCode(err error) string {
	if errors.Is(err, process.ErrProcessTreeUnknown) || errors.Is(err, gitops.ErrIntegrationUncertain) {
		return "compose_uncertain"
	}
	var gitErr *gitops.Error
	if errors.As(err, &gitErr) {
		return string(gitErr.Code)
	}
	return "candidate_compose_failed"
}

// integrationPreflight refuses replay after an integration intent or an
// unclosed original Git process. Completed preflight Git records remain usable.
func (j *durableJournal) integrationPreflight(ctx context.Context) error {
	bad := errors.New("gitops_journal_unsafe")
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	held, heldErr := j.file.Stat()
	if err != nil || heldErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, held) || info.Size() > 16*1024*1024 {
		return bad
	}
	body, err := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
	if err != nil {
		return err
	}
	if int64(len(body)) != info.Size() || len(body) > 16*1024*1024 || len(body) > 0 && body[len(body)-1] != '\n' {
		return bad
	}
	var pending *gitops.GitProcessRecord
	for _, line := range bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n")) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(body) == 0 {
			break
		}
		var record struct {
			Version int             `json:"version"`
			Kind    string          `json:"kind"`
			Value   json.RawMessage `json:"value"`
		}
		if decode(line, &record) != nil || record.Version != 1 || record.Kind == "" || len(record.Value) == 0 {
			return bad
		}
		if record.Kind == "integration_intent" {
			return gitops.ErrIntegrationUncertain
		}
		if record.Kind != "git_process" {
			continue
		}
		var p gitops.GitProcessRecord
		if decode(record.Value, &p) != nil || len(p.CommandDigest) != 64 {
			return bad
		}
		if _, err := hex.DecodeString(p.CommandDigest); err != nil {
			return bad
		}
		switch p.Phase {
		case "intent":
			if pending != nil || p.Process != nil {
				return bad
			}
			pending = &p
		case "spawned":
			if pending == nil || pending.Phase != "intent" || pending.CommandDigest != p.CommandDigest || p.Process == nil || p.Process.PID <= 0 || p.Process.PGID != p.Process.PID || !p.Process.BirthKnown || p.Process.Birth == "" || !filepath.IsAbs(p.Process.Executable) || len(p.Process.ExecutableSHA256) != 64 {
				return bad
			}
			if _, err := hex.DecodeString(p.Process.ExecutableSHA256); err != nil {
				return bad
			}
			if _, err := time.Parse(time.RFC3339Nano, p.Process.StartedAt); err != nil {
				return bad
			}
			pending = &p
		case "exited":
			if pending == nil || pending.Phase != "spawned" || pending.CommandDigest != p.CommandDigest || p.Process == nil || *pending.Process != *p.Process {
				return bad
			}
			pending = nil
		case "unknown":
			return gitops.ErrIntegrationUncertain
		default:
			return bad
		}
	}
	if pending != nil {
		return gitops.ErrIntegrationUncertain
	}
	return nil
}

// composePreflight refuses replay of any previous composition attempt. A
// partially recorded candidate action is ownership-unknown: retain the
// worktree/ref and make the owner reconcile it explicitly.
func (j *durableJournal) composePreflight(ctx context.Context) error {
	if err := j.integrationPreflight(ctx); err != nil {
		return process.ErrProcessTreeUnknown
	}
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	held, heldErr := j.file.Stat()
	if err != nil || heldErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, held) || info.Size() > 16*1024*1024 {
		return errors.New("gitops_journal_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
	if err != nil || int64(len(body)) != info.Size() || len(body) > 16*1024*1024 || len(body) > 0 && body[len(body)-1] != '\n' {
		return errors.New("gitops_journal_unsafe")
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Version int             `json:"version"`
			Kind    string          `json:"kind"`
			Value   json.RawMessage `json:"value"`
		}
		if decode(line, &record) != nil || record.Version != 1 || record.Kind == "" || len(record.Value) == 0 {
			return errors.New("gitops_journal_unsafe")
		}
		switch record.Kind {
		case "candidate_intent", "candidate_ref_intent", "candidate_receipt":
			return process.ErrProcessTreeUnknown
		}
	}
	return nil
}

type durableJournal struct {
	file   *os.File
	lock   *os.File
	dir    *os.File // held journal directory; seal paths use openat through it
	lockMu sync.Mutex
}

// OpenJournal shares the existing Git Host journal with managed workspace execution.
func OpenJournal(root string) (*durableJournal, error) { return openJournal(root) }

// OpenFreshJournal grants only this invocation a new journal. Even an empty
// existing file is cold state; callers must retain it rather than resume it.
func OpenFreshJournal(root string) (*durableJournal, error) {
	return openJournalFlags(root, os.O_EXCL)
}
func (j *durableJournal) Close() error {
	if j.dir != nil {
		return errors.Join(j.lock.Close(), j.file.Close(), j.dir.Close())
	}
	return errors.Join(j.lock.Close(), j.file.Close())
}

func (j *durableJournal) RecordGitProcess(_ context.Context, record gitops.GitProcessRecord) error {
	return j.append("git_process", record)
}

// A managed integration retry must not replay an existing merge intent.
// The journal is the existing durable operation ledger, not a second state store.
func (j *durableJournal) IntegrationStarted(ctx context.Context) (bool, error) {
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() > 16*1024*1024 {
		return false, errors.New("gitops_journal_limit")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16*1024*1024+1))
	found := false
	for {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		var record struct {
			Kind string `json:"kind"`
		}
		err = decoder.Decode(&record)
		if err == io.EOF {
			return found, nil
		}
		if err != nil {
			return false, err
		}
		if record.Kind == "integration_intent" {
			found = true
		}
	}
}

func (j *durableJournal) RecordCandidateAction(_ context.Context, phase string, value any) error {
	return j.append("candidate_action_"+phase, value)
}

func (j *durableJournal) RecordFilteredTargetObservation(_ context.Context, observation gitops.FilteredTargetObservationV1) error {
	return j.append("filter_target_observed", observation)
}

func (j *durableJournal) RecordFilterMaterialized(_ context.Context, receipt gitops.MaterializeReceipt) error {
	return j.append("filter_materialized", receipt)
}

func (j *durableJournal) RecordWorkspacePrepared(_ context.Context, workspace gitops.PreparedWorkspace) error {
	return j.append("workspace_prepared", workspace)
}

func openJournal(root string) (*durableJournal, error) {
	return openJournalFlags(root, 0)
}

func openJournalFlags(root string, extra int) (*durableJournal, error) {
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	if sourceJournalPath(root) {
		return openSourceJournal(root, extra)
	}
	path := filepath.Join(root, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW|extra, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, errors.New("gitops_journal_unsafe")
	}
	if extra&os.O_EXCL != 0 {
		named, namedErr := os.Lstat(path)
		owner, ok := info.Sys().(*syscall.Stat_t)
		if namedErr != nil || !os.SameFile(named, info) || !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 || info.Size() != 0 {
			_ = file.Close()
			return nil, errors.New("gitops_journal_unsafe")
		}
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
	lock, err := os.OpenFile(filepath.Join(root, "events.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		_ = file.Close()
		return nil, err
	}
	owner, owned := lockInfo.Sys().(*syscall.Stat_t)
	if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 || !owned || owner.Uid != uint32(os.Geteuid()) {
		_ = lock.Close()
		_ = file.Close()
		return nil, errors.New("gitops_journal_unsafe")
	}
	directory, err = os.Open(root)
	if err != nil {
		_ = lock.Close()
		_ = file.Close()
		return nil, err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		_ = lock.Close()
		_ = file.Close()
		return nil, err
	}
	return &durableJournal{file: file, lock: lock}, nil
}

func (j *durableJournal) close() { _ = j.Close() }
func (j *durableJournal) append(kind string, value any) error {
	body, err := json.Marshal(map[string]any{"version": 1, "kind": kind, "value": value})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > journalRecordLimit {
		return errors.New("gitops_journal_record_too_large")
	}
	var row journalRecord
	if err = json.Unmarshal(body, &row); err != nil {
		return err
	}
	return j.withLock(syscall.LOCK_EX, func() error {
		// A seal is an immutable source-journal fence.  Check it while holding
		// events.lock and before any admission or write; even a damaged seal
		// remains fail-closed until an owner resolves the retained evidence.
		if _, found, sealErr := readCandidateSeal(filepath.Join(filepath.Dir(j.file.Name()), "seal.json")); sealErr != nil {
			return errCandidateSeal
		} else if found {
			return errCandidateSeal
		}
		st, err := j.validJournalFile()
		if err != nil {
			return err
		}
		state, err := j.cleanupStateLocked(context.Background())
		if err != nil {
			return err
		}
		newPlan := kind == "cleanup_plan" && state.Plan == nil
		changed, err := applyCleanupRecord(&state, row)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		reserve := cleanupReservedBytes(state)
		// Plan admission also pays for bounded Git-process observations. Those may
		// consume this allowance, but never the remaining action/final records.
		if newPlan {
			reserve += 60 * 1024
		}
		if st.Size()+int64(len(body))+reserve > journalLimit {
			return errors.New("gitops_journal_full")
		}
		for written := 0; written < len(body); {
			n, e := j.file.Write(body[written:])
			written += n
			if e != nil {
				return e
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
		if err = j.file.Sync(); err != nil {
			return err
		}
		latest, err := j.validJournalFile()
		if err != nil {
			return err
		}
		if latest.Size() != st.Size()+int64(len(body)) {
			return errors.New("gitops_journal_changed")
		}
		return nil
	})
}
func privateJournalFile(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && ok && owner.Uid == uint32(os.Geteuid()) && owner.Nlink == 1
}
func (j *durableJournal) validJournalFile() (os.FileInfo, error) {
	held, err := j.file.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(j.file.Name())
	if err != nil || !os.SameFile(held, named) || !privateJournalFile(named) || !privateJournalFile(held) || held.Size() != named.Size() {
		return nil, errors.New("gitops_journal_replaced")
	}
	if held.Size() > journalLimit {
		return nil, errors.New("gitops_journal_unsafe")
	}
	return held, nil
}
func (j *durableJournal) withLock(mode int, fn func() error) error {
	j.lockMu.Lock()
	defer j.lockMu.Unlock()
	held, err := j.lock.Stat()
	if err != nil {
		return err
	}
	valid := func() error {
		current, e := j.lock.Stat()
		named, namedErr := os.Lstat(j.lock.Name())
		if e != nil || namedErr != nil || !privateJournalFile(current) || !privateJournalFile(named) || !os.SameFile(held, current) || !os.SameFile(held, named) {
			return errors.New("gitops_journal_lock_replaced")
		}
		return nil
	}
	if err = valid(); err != nil {
		return err
	}
	if err = syscall.Flock(int(j.lock.Fd()), mode|syscall.LOCK_NB); err != nil {
		return err
	}
	defer syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	if err = valid(); err != nil {
		return err
	}
	if err = fn(); err != nil {
		return err
	}
	return valid()
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
func (j *durableJournal) PersistCleanupPlan(_ context.Context, value gitops.CleanupPlan) error {
	return j.append("cleanup_plan", value)
}
func (j *durableJournal) PersistCleanupStep(_ context.Context, value gitops.CleanupStep) error {
	return j.append("cleanup_step", value)
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
var _ gitops.CleanupRecoveryJournal = (*durableJournal)(nil)
