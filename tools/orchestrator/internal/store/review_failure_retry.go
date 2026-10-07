package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

// This is explicit owner authority for one readonly review failure, not a
// never-started proof or permission to retry a writable candidate stage.
func validateReviewFailure(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	bad := CodeError("candidate_review_retry_unsupported")
	if e.Kind != contract.EventFailed || e.Process == nil || e.Process.PID <= 0 || e.Process.PGID <= 0 || e.Process.Birth == "" || e.ExitCode == nil || *e.ExitCode < 0 || e.Artifact == nil {
		return bad
	}
	var launch, status, outcome, database string
	if tx.QueryRowContext(ctx, `SELECT h.launch_id,s.status,s.outcome FROM segment_runtime s JOIN runtime_hosts h ON h.id=s.host_id WHERE s.segment_id=? AND s.host_id=? AND s.command_id=? AND s.attempt_id=?`, e.SegmentID, e.ProducerID, e.CommandID, e.AttemptID).Scan(&launch, &status, &outcome) != nil || status != "exited" || outcome != "failed" {
		return bad
	}
	var accepted int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_decisions d JOIN runtime_events e ON e.event_id=d.event_id WHERE e.segment_id=?`, e.SegmentID).Scan(&accepted) != nil || accepted != 0 {
		return bad
	}
	rows, err := tx.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE segment_id=?`, e.SegmentID)
	if err != nil {
		return err
	}
	exits, failures, running := 0, 0, 0
	valid := true
	for rows.Next() {
		var raw string
		var v contract.Event
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &v) != nil || v.ProducerID != e.ProducerID || v.TaskID != e.TaskID || v.RunID != e.RunID || v.AttemptID != e.AttemptID || v.SegmentID != e.SegmentID || v.CommandID != e.CommandID || v.WorkRevision != e.WorkRevision {
			valid = false
			break
		}
		switch v.Kind {
		case contract.EventFailed:
			failures++
			valid = v.EventID == e.EventID && v.Sequence == e.Sequence && v.PayloadHash == e.PayloadHash && reflect.DeepEqual(v.Artifact, e.Artifact) && reflect.DeepEqual(v.Process, e.Process) && v.ExitCode != nil && *v.ExitCode == *e.ExitCode
		case contract.EventExited:
			exits++
			valid = v.Sequence > e.Sequence && reflect.DeepEqual(v.Artifact, e.Artifact) && reflect.DeepEqual(v.Process, e.Process) && v.ExitCode != nil && *v.ExitCode == *e.ExitCode
		case contract.EventRunning:
			running++
			valid = v.Sequence < e.Sequence && reflect.DeepEqual(v.Process, e.Process)
		case contract.EventPrepared, contract.EventSpawned, contract.EventSession:
			valid = v.Sequence < e.Sequence
		default:
			valid = false
		}
		if !valid {
			break
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !valid || exits != 1 || failures != 1 || running < 1 {
		return bad
	}
	// SQLite's main filename is the DB actually owning this transaction. Do
	// not derive the trusted spool root from the event's supplied path.
	if tx.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&database) != nil || !filepath.IsAbs(database) {
		return bad
	}
	validateBody := func(body []byte) error {
		version, err := validateReviewFailureBody(body)
		if err != nil || version == 0 && *e.ExitCode <= 0 {
			return bad
		}
		if version == 2 {
			return validateReviewFailureV2Lifecycle(ctx, tx, e)
		}
		return nil
	}
	if e.Artifact.HostArtifact != nil {
		body, err := readCommittedHostArtifact(ctx, tx, e, 256)
		if err != nil {
			return bad
		}
		return validateBody(body)
	}
	parts := []string{"host-spool", launch, e.AttemptID, e.SegmentID, "result-output.bin"}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return bad
		}
	}
	root := filepath.Dir(database)
	expected := filepath.Join(append([]string{root}, parts...)...)
	r := e.Artifact
	if r.ID != "result-output.bin" || r.Path != expected || r.Size < 1 || r.Size > 256 || !validDigest(r.SHA256) || r.SHA256 != e.PayloadHash {
		return bad
	}
	file, err := openReviewDiagnostic(root, parts)
	if err != nil {
		return bad
	}
	defer file.Close()
	var before, after unix.Stat_t
	if unix.Fstat(int(file.Fd()), &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0777 != 0600 || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size != r.Size {
		return bad
	}
	body, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || unix.Fstat(int(file.Fd()), &after) != nil || before.Size != after.Size || before.Mode != after.Mode || before.Mtim != after.Mtim || before.Ctim != after.Ctim || int64(len(body)) != r.Size || runtimeHash(string(body)) != r.SHA256 {
		return bad
	}
	// Verify that the no-follow name chain still selects the inode we read.
	linked, err := openReviewDiagnostic(root, parts)
	if err != nil {
		return bad
	}
	var current unix.Stat_t
	err = unix.Fstat(int(linked.Fd()), &current)
	linked.Close()
	if err != nil || current.Dev != before.Dev || current.Ino != before.Ino || current.Size != before.Size || current.Mode != before.Mode || current.Uid != before.Uid || current.Nlink != 1 || current.Mtim != before.Mtim || current.Ctim != before.Ctim {
		return bad
	}
	return validateBody(body)
}

func validateReviewFailureBody(body []byte) (int, error) {
	bad := CodeError("candidate_review_retry_unsupported")
	var diagnostic struct {
		Status        string          `json:"status"`
		Reason        string          `json:"reason"`
		Code          string          `json:"code"`
		WriterVersion json.RawMessage `json:"writer_version"`
	}
	if strictDecisionJSON(string(body), &diagnostic) != nil || diagnostic.Status != "failed" || diagnostic.Reason != "candidate_freeze_failed" || diagnostic.Code != "candidate_review_invalid" {
		return 0, bad
	}
	if diagnostic.WriterVersion == nil {
		return 0, nil
	}
	var version int
	if json.Unmarshal(diagnostic.WriterVersion, &version) != nil || version != 2 {
		return 0, bad
	}
	return version, nil
}

// v2 is selected only after reading the authenticated diagnostic. Legacy
// history retains its original proof rules; it cannot acquire v2 authority.
func validateReviewFailureV2Lifecycle(ctx context.Context, tx *sql.Tx, failed contract.Event) error {
	bad := CodeError("candidate_review_retry_unsupported")
	rows, err := tx.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE segment_id=? ORDER BY sequence`, failed.SegmentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	kinds := []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventFailed, contract.EventExited}
	phase, sequence := 0, int64(0)
	sessions := 0
	for rows.Next() {
		var raw string
		var event contract.Event
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &event) != nil || event.Sequence <= sequence || event.RunID != failed.RunID || event.TaskID != failed.TaskID || event.AttemptID != failed.AttemptID || event.SegmentID != failed.SegmentID || event.CommandID != failed.CommandID || event.WorkRevision != failed.WorkRevision || event.ProducerID != failed.ProducerID {
			return bad
		}
		sequence = event.Sequence
		if event.Kind == contract.EventSession {
			sessions++
			// Host installs the session observer after Running, and closes its
			// output stream before emitting the terminal failure.
			if sessions != 1 || phase != 3 || !reflect.DeepEqual(event.Process, failed.Process) || event.Artifact != nil || event.ExitCode != nil || event.SessionKind == "" || event.SessionID == "" || event.SessionKind != failed.SessionKind || event.SessionID != failed.SessionID {
				return bad
			}
			continue
		}
		if phase >= len(kinds) || event.Kind != kinds[phase] {
			return bad
		}
		phase++
		switch event.Kind {
		case contract.EventPrepared:
			if event.Process != nil || event.Artifact != nil || event.ExitCode != nil {
				return bad
			}
		case contract.EventSpawned, contract.EventRunning:
			if !reflect.DeepEqual(event.Process, failed.Process) || event.Artifact != nil || event.ExitCode != nil {
				return bad
			}
		case contract.EventFailed:
			if event.EventID != failed.EventID || event.Sequence != failed.Sequence || event.PayloadHash != failed.PayloadHash || event.PayloadHash != failed.Artifact.SHA256 || !reflect.DeepEqual(event.Artifact, failed.Artifact) || !reflect.DeepEqual(event.Process, failed.Process) || event.ExitCode == nil || *event.ExitCode != *failed.ExitCode {
				return bad
			}
		case contract.EventExited:
			// Exited hashes its lifecycle marker, not the diagnostic body, and
			// retains the actual worker exit (including zero for schema errors).
			if event.PayloadHash != runtimeHash("exited:"+failed.SegmentID) || !reflect.DeepEqual(event.Artifact, failed.Artifact) || !reflect.DeepEqual(event.Process, failed.Process) || event.ExitCode == nil || *event.ExitCode != *failed.ExitCode {
				return bad
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if phase != len(kinds) {
		return bad
	}
	return nil
}

func openReviewDiagnostic(root string, parts []string) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Open(root, flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), root)
	defer func() {
		if current != nil {
			current.Close()
		}
	}()
	for i, part := range parts {
		var stat unix.Stat_t
		if err = unix.Fstat(int(current.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 || stat.Uid != uint32(os.Geteuid()) {
			return nil, ErrConflict
		}
		nextFlags := flags
		if i < len(parts)-1 {
			nextFlags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(int(current.Fd()), part, nextFlags, 0)
		if err != nil {
			return nil, err
		}
		current.Close()
		current = os.NewFile(uintptr(next), part)
	}
	file := current
	current = nil
	return file, nil
}
