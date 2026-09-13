package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const (
	ErrDependencyBlocked CodeError = "dependency_blocked"
	ErrAttemptLimit      CodeError = "attempt_limit"
	ErrNotFound          CodeError = "not_found"
	ErrConflict          CodeError = "conflict"
)

type DB struct {
	sql                 *sql.DB
	path                string
	mu                  sync.Mutex
	runControlLimit     int64
	userControlLimit    int64
	runProgressLimit    int64
	userProgressLimit   int64
	reportControlLimit  int64
	reportProgressLimit int64
	minFreeBytes        uint64
	criticalDiskReserve uint64
	sqliteWriteOverhead uint64
	storageAvailable    func(string) (uint64, error)
}

type RunSpec struct {
	ID, ControllerThread string
	PlanRevision         int
	OriginContextID      string
	OriginPID            int
	OriginBirth          string
}
type TaskSpec struct {
	ID, RunID              string
	Dependencies           []string
	MaxAttempts            int
	WorkRevision           int
	BudgetGroupID          string
	MaxActiveMS            int64
	AdapterPayload         json.RawMessage
	FallbackPayloads       []json.RawMessage
	CompletionPolicy       string
	ExpectedArtifactSHA256 string
}
type Attempt struct {
	ID, TaskID, SegmentID, Status string
	AttemptNo                     int
}

func Open(path string) (*DB, error) {
	if !filepath.IsAbs(path) {
		return nil, CodeError("path_not_absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if createErr != nil {
			return nil, createErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !storeOwned(info) {
		return nil, CodeError("untrusted_database")
	}
	u := &url.URL{Scheme: "file", Path: path}
	u.RawQuery = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	out := &DB{
		sql: d, path: path,
		runControlLimit: 16 * 1024 * 1024, userControlLimit: 128 * 1024 * 1024,
		runProgressLimit: 12 * 1024 * 1024, userProgressLimit: 96 * 1024 * 1024,
		reportControlLimit: 20 * 1024 * 1024, reportProgressLimit: 16 * 1024 * 1024,
		minFreeBytes: 64 * 1024 * 1024, criticalDiskReserve: 1024 * 1024, sqliteWriteOverhead: 4 * 1024 * 1024,
		storageAvailable: filesystemAvailable,
	}
	if err := out.migrate(); err != nil {
		d.Close()
		return nil, err
	}
	if err := out.ensureRuntimeSchema(); err != nil {
		d.Close()
		return nil, err
	}
	return out, nil
}

func filesystemAvailable(path string) (uint64, error) {
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(path, &filesystem); err != nil {
		return 0, err
	}
	return uint64(filesystem.Bavail) * uint64(filesystem.Bsize), nil
}

func storeOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
func (d *DB) Close() error { return d.sql.Close() }
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;`); err != nil {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS schema_meta(version INTEGER NOT NULL)`); err != nil {
		return rollback(err)
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return rollback(err)
	}
	if err == nil && version > 1 {
		return rollback(CodeError("schema_newer_than_binary"))
	}
	if err == nil && version < 1 {
		return rollback(CodeError("schema_invalid"))
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS coordinator(id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, controller_thread TEXT NOT NULL, plan_revision INTEGER NOT NULL, origin_context_id TEXT NOT NULL, origin_pid INTEGER NOT NULL, origin_birth TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), status TEXT NOT NULL, dependencies_json TEXT NOT NULL, max_attempts INTEGER NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), attempt_no INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(task_id, attempt_no))`,
		`CREATE TABLE IF NOT EXISTS segments(id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), segment_no INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(attempt_id, segment_no))`,
		`CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload_hash TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(attempt_id, sequence))`,
		`CREATE TABLE IF NOT EXISTS questions(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), revision INTEGER NOT NULL, status TEXT NOT NULL, answer_hash TEXT, created_at TEXT NOT NULL, UNIQUE(task_id, revision))`,
		`CREATE TABLE IF NOT EXISTS deliveries(id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), target_thread TEXT NOT NULL, epoch INTEGER NOT NULL, status TEXT NOT NULL, payload_hash TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS budget_reservations(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id), segment_id TEXT NOT NULL, status TEXT NOT NULL, active_ms INTEGER NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS host_instances(id TEXT PRIMARY KEY, origin_context_id TEXT NOT NULL, generation INTEGER NOT NULL, process_pid INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), sequence INTEGER NOT NULL, payload_hash TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(attempt_id, sequence))`,
		`CREATE TABLE IF NOT EXISTS cleanup_entries(id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL REFERENCES attempts(id), path TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
	}
	for _, stmt := range stmts {
		if _, err = tx.Exec(stmt); err != nil {
			return rollback(err)
		}
	}
	if fresh {
		if _, err = tx.Exec(`INSERT INTO schema_meta(version) VALUES(1)`); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (d *DB) CreateRun(ctx context.Context, s RunSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.ID == "" || s.ControllerThread == "" || s.PlanRevision < 1 || s.OriginContextID == "" || s.OriginPID <= 0 || s.OriginBirth == "" {
		return CodeError("invalid_run")
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, s.ID, s.ControllerThread, s.PlanRevision, s.OriginContextID, s.OriginPID, s.OriginBirth, "queued", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	return nil
}
func (d *DB) CreateTask(ctx context.Context, s TaskSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.ID == "" || s.RunID == "" || s.MaxAttempts < 1 || s.MaxAttempts > 3 {
		return CodeError("invalid_task")
	}
	deps, _ := json.Marshal(s.Dependencies)
	_, err := d.sql.ExecContext(ctx, `INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES(?,?,?,?,?,?)`, s.ID, s.RunID, "queued", string(deps), s.MaxAttempts, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	return nil
}
func (d *DB) StartAttempt(ctx context.Context, taskID, segmentID string) (Attempt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if taskID == "" || segmentID == "" {
		return Attempt{}, CodeError("invalid_attempt")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	rollback := func(e error) (Attempt, error) { _ = tx.Rollback(); return Attempt{}, e }
	var status string
	var depsJSON string
	var max int
	if err = tx.QueryRowContext(ctx, `SELECT status,dependencies_json,max_attempts FROM tasks WHERE id=?`, taskID).Scan(&status, &depsJSON, &max); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollback(ErrNotFound)
		}
		return rollback(err)
	}
	var active string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM attempts WHERE task_id=? AND status IN ('running','stopping','unknown') LIMIT 1`, taskID).Scan(&active); err == nil {
		return rollback(ErrConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var deps []string
	if err = json.Unmarshal([]byte(depsJSON), &deps); err != nil {
		return rollback(CodeError("invalid_task"))
	}
	for _, dep := range deps {
		var ds string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, dep).Scan(&ds); err != nil || (ds != "completed" && ds != "integrated") {
			return rollback(ErrDependencyBlocked)
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id=?`, taskID).Scan(&count); err != nil {
		return rollback(err)
	}
	if count >= max || count >= 3 {
		return rollback(ErrAttemptLimit)
	}
	id, err := newID("attempt")
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES(?,?,?,?,?)`, id, taskID, count+1, "running", now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES(?,?,?,?,?)`, segmentID, id, 1, "running", now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='running' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return Attempt{}, err
	}
	return Attempt{ID: id, TaskID: taskID, SegmentID: segmentID, AttemptNo: count + 1, Status: "running"}, nil
}
func (d *DB) StartSegment(ctx context.Context, attemptID, segmentID string) (Attempt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	rollback := func(e error) (Attempt, error) { _ = tx.Rollback(); return Attempt{}, e }
	var taskID, status string
	var attemptNo int
	if err = tx.QueryRowContext(ctx, `SELECT task_id,status,attempt_no FROM attempts WHERE id=?`, attemptID).Scan(&taskID, &status, &attemptNo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rollback(ErrNotFound)
		}
		return rollback(err)
	}
	if status != "result_ready" && status != "interrupted" && status != "acknowledged" {
		return rollback(ErrConflict)
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segments WHERE attempt_id=?`, attemptID).Scan(&n); err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES(?,?,?,?,?)`, segmentID, attemptID, n+1, "running", now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='running' WHERE id=?`, attemptID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return Attempt{}, err
	}
	return Attempt{ID: attemptID, TaskID: taskID, SegmentID: segmentID, AttemptNo: attemptNo, Status: "running"}, nil
}

func (d *DB) FinishAttempt(ctx context.Context, id, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == "" || (status != "result_ready" && status != "failed" && status != "cancelled" && status != "completed" && status != "interrupted" && status != "unknown") {
		return CodeError("invalid_status")
	}
	result, err := d.sql.ExecContext(ctx, `UPDATE attempts SET status=? WHERE id=? AND status='running'`, status, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (d *DB) Attempt(ctx context.Context, id string) (Attempt, error) {
	var a Attempt
	err := d.sql.QueryRowContext(ctx, `SELECT id,task_id,status,attempt_no,(SELECT id FROM segments WHERE attempt_id=attempts.id ORDER BY segment_no LIMIT 1) FROM attempts WHERE id=?`, id).Scan(&a.ID, &a.TaskID, &a.Status, &a.AttemptNo, &a.SegmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}
func newID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}

func (d *DB) AcknowledgeAttempt(ctx context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.sql.ExecContext(ctx, `UPDATE attempts SET status='acknowledged' WHERE id=? AND status='result_ready'`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}

type Question struct {
	ID, TaskID         string
	Revision           int
	Status, AnswerHash string
}

func (d *DB) CreateQuestion(ctx context.Context, id, taskID string, revision int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == "" || taskID == "" || revision < 1 {
		return CodeError("invalid_question")
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO questions(id,task_id,revision,status,created_at) VALUES(?,?,?,?,?)`, id, taskID, revision, "open", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	return nil
}
func (d *DB) LatestAttempt(ctx context.Context, taskID string) (Attempt, error) {
	var a Attempt
	err := d.sql.QueryRowContext(ctx, `SELECT id,task_id,status,attempt_no,(SELECT id FROM segments WHERE attempt_id=attempts.id ORDER BY segment_no DESC LIMIT 1) FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1`, taskID).Scan(&a.ID, &a.TaskID, &a.Status, &a.AttemptNo, &a.SegmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}
func (d *DB) RunID(ctx context.Context, taskID string) (string, error) {
	var id string
	err := d.sql.QueryRowContext(ctx, `SELECT run_id FROM tasks WHERE id=?`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
