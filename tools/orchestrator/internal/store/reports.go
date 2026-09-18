package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func (d *DB) RegisterReportCapability(ctx context.Context, registration contract.ReportCapabilityRegistration) (contract.DurableAck, error) {
	if registration.CapabilityID == "" || registration.ProducerID == "" || registration.SegmentID == "" || registration.TokenHash == "" || !validDigest(registration.TokenHash) || registration.WorkRevision < 1 || registration.ExecutionEpoch == 0 || !filepath.IsAbs(registration.CapabilityDir) {
		return contract.DurableAck{}, CodeError("invalid_report_capability")
	}
	info, err := os.Lstat(registration.CapabilityDir)
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return contract.DurableAck{}, CodeError("unsafe_report_capability")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var segmentID, producerID, runID, taskID, attemptID, tokenHash, capabilityDir, status string
	var revision int
	var hostEpoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT c.segment_id,c.producer_id,c.run_id,c.task_id,c.attempt_id,c.work_revision,c.token_hash,c.capability_dir,c.status,h.coordinator_epoch FROM report_capabilities c JOIN runtime_hosts h ON h.id=c.producer_id WHERE c.capability_id=? AND h.status IN ('ready','reconciling')`, registration.CapabilityID).Scan(&segmentID, &producerID, &runID, &taskID, &attemptID, &revision, &tokenHash, &capabilityDir, &status, &hostEpoch); err != nil || segmentID != registration.SegmentID || producerID != registration.ProducerID || runID != registration.RunID || taskID != registration.TaskID || attemptID != registration.AttemptID || revision != registration.WorkRevision || hostEpoch != registration.ExecutionEpoch {
		return rollback(ErrConflict)
	}
	if status == "registered" {
		if tokenHash != strings.ToLower(registration.TokenHash) || capabilityDir != registration.CapabilityDir {
			return rollback(ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET execution_epoch=? WHERE capability_id=?`, registration.ExecutionEpoch, registration.CapabilityID); err != nil {
			return rollback(err)
		}
		if err = tx.Commit(); err != nil {
			return contract.DurableAck{}, err
		}
		return contract.DurableAck{ProducerID: producerID, EventID: registration.CapabilityID, PayloadHash: tokenHash, Status: "durable"}, nil
	}
	if status != "pending" {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET token_hash=?,capability_dir=?,execution_epoch=?,status='registered' WHERE capability_id=?`, strings.ToLower(registration.TokenHash), registration.CapabilityDir, registration.ExecutionEpoch, registration.CapabilityID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: producerID, EventID: registration.CapabilityID, PayloadHash: strings.ToLower(registration.TokenHash), Status: "durable"}, nil
}

func infoSyscall(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

type ReportEventSpec struct {
	CapabilityID string
	Token        string
	EventID      string
	Sequence     int64
	Kind         string
	Payload      json.RawMessage
}

var reportIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (d *DB) CommitReportEvent(ctx context.Context, spec ReportEventSpec) (contract.DurableAck, error) {
	if spec.CapabilityID == "" || spec.Token == "" || !reportIDPattern.MatchString(spec.EventID) || spec.Sequence < 1 || len(spec.Payload) == 0 || len(spec.Payload) > 60*1024 || !json.Valid(spec.Payload) {
		return contract.DurableAck{}, CodeError("invalid_report_event")
	}
	if spec.Kind != "progress" && spec.Kind != "question" && spec.Kind != "result" && spec.Kind != "failure" {
		return contract.DurableAck{}, CodeError("invalid_report_kind")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var tokenHash, capabilityDir, runID, taskID, attemptID, segmentID, producerID, status string
	var revision int
	var epoch uint64
	var used, progressUsed int64
	var hostEpoch uint64
	var hostStatus string
	if err = tx.QueryRowContext(ctx, `SELECT c.token_hash,c.capability_dir,c.run_id,c.task_id,c.attempt_id,c.segment_id,c.producer_id,c.work_revision,c.execution_epoch,c.status,c.used_bytes,COALESCE((SELECT progress_bytes FROM storage_usage WHERE scope='capability' AND scope_id=c.capability_id),0),h.coordinator_epoch,h.status FROM report_capabilities c JOIN runtime_hosts h ON h.id=c.producer_id WHERE c.capability_id=?`, spec.CapabilityID).Scan(&tokenHash, &capabilityDir, &runID, &taskID, &attemptID, &segmentID, &producerID, &revision, &epoch, &status, &used, &progressUsed, &hostEpoch, &hostStatus); err != nil || (status != "registered" && status != "revoked") || hostStatus != "ready" || hostEpoch != epoch || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(spec.Token))) != 1 {
		return rollback(CodeError("report_capability_rejected"))
	}
	canonicalPayload, err := canonicalReportPayload(spec.Payload)
	if err != nil {
		return rollback(CodeError("invalid_report_event"))
	}
	payloadHash := runtimeHash(string(canonicalPayload))
	var oldKind, oldHash, oldBody string
	duplicate := false
	if err = tx.QueryRowContext(ctx, `SELECT kind,payload_hash,body_json FROM report_events WHERE capability_id=? AND sequence=? AND event_id=?`, spec.CapabilityID, spec.Sequence, spec.EventID).Scan(&oldKind, &oldHash, &oldBody); err == nil {
		if oldKind != spec.Kind || oldHash != payloadHash {
			return rollback(ErrConflict)
		}
		duplicate = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	if !duplicate && status == "revoked" {
		return rollback(CodeError("report_capability_rejected"))
	}
	if !duplicate {
		var last int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM report_events WHERE capability_id=?`, spec.CapabilityID).Scan(&last); err != nil || spec.Sequence != last+1 {
			return rollback(ErrEventSequence)
		}
	}
	var event contract.Event
	if duplicate {
		event, err = decodeReportPayload(spec.Kind, canonicalPayload)
	} else {
		event, err = validateReportPayload(spec.Kind, canonicalPayload, capabilityDir)
	}
	if err != nil {
		return rollback(err)
	}
	event.Version, event.ProducerID, event.EventID = 1, spec.CapabilityID, spec.EventID
	event.RunID, event.TaskID, event.AttemptID, event.SegmentID = runID, taskID, attemptID, segmentID
	eventKind := spec.Kind
	if eventKind == "failure" {
		eventKind = contract.EventFailed
	}
	event.WorkRevision, event.ExecutionEpoch, event.Sequence, event.Kind, event.PayloadHash = revision, epoch, spec.Sequence, eventKind, payloadHash
	if spec.Kind == "question" {
		sessionErr := tx.QueryRowContext(ctx, `SELECT session_kind,session_id FROM segment_sessions WHERE segment_id=?`, segmentID).Scan(&event.SessionKind, &event.SessionID)
		if sessionErr != nil && !errors.Is(sessionErr, sql.ErrNoRows) {
			return rollback(sessionErr)
		}
	}
	body, _ := json.Marshal(event)
	if duplicate {
		oldCanonical, oldErr := canonicalReportEvent([]byte(oldBody))
		newCanonical, newErr := canonicalReportEvent(body)
		if oldErr != nil || newErr != nil || subtle.ConstantTimeCompare(oldCanonical, newCanonical) != 1 {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return contract.DurableAck{ProducerID: spec.CapabilityID, AckedThrough: spec.Sequence, EventID: spec.EventID, PayloadHash: payloadHash, Status: "durable"}, nil
	}
	diagnostic := spec.Kind == "progress"
	accountedBytes := int64(len(body))
	if event.Artifact != nil {
		accountedBytes += event.Artifact.Size
	}
	if spec.Kind == "question" && event.SessionID == "" {
		accountedBytes += 4096
	}
	if err = d.storageAdmission(ctx, tx, runID, accountedBytes, diagnostic); err != nil {
		return rollback(err)
	}
	delivery := "pending"
	if diagnostic {
		delivery = "internal"
	} else if spec.Kind == "question" && event.SessionID == "" {
		delivery = "pending_session"
	}
	actionSlot := "action-" + runtimeHash(spec.CapabilityID + "\x00" + spec.EventID)[:32]
	if accountedBytes > d.reportControlLimit || used > d.reportControlLimit-accountedBytes || (diagnostic && (accountedBytes > d.reportProgressLimit || progressUsed > d.reportProgressLimit-accountedBytes)) {
		return rollback(CodeError("report_budget_exhausted"))
	}
	if spec.Kind == "question" {
		var openQuestions int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started')) + (SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND e.kind='question' AND e.delivery_status='pending_session')`, taskID, taskID).Scan(&openQuestions); err != nil || openQuestions != 0 {
			if err == nil {
				err = CodeError("question_already_open")
			}
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, spec.EventID, spec.CapabilityID, spec.Sequence, spec.Kind, payloadHash, string(body), spec.Sequence, actionSlot, delivery, accountedBytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	if delivery != "pending_session" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, spec.EventID, taskID); err != nil {
			return rollback(mapConflict(err))
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET used_bytes=used_bytes+? WHERE capability_id=?`, accountedBytes, spec.CapabilityID); err != nil {
		return rollback(err)
	}
	if spec.Kind == "question" && event.SessionID != "" {
		if err = applyEvent(ctx, tx, event); err != nil {
			return rollback(err)
		}
		if err = ensureQuestionStop(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: spec.CapabilityID, AckedThrough: spec.Sequence, EventID: spec.EventID, PayloadHash: payloadHash, Status: "durable"}, nil
}

func canonicalReportEvent(body []byte) ([]byte, error) {
	var event contract.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	event.ExecutionEpoch = 0
	return json.Marshal(event)
}

func canonicalReportPayload(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func validateReportPayload(kind string, raw json.RawMessage, capabilityDir string) (contract.Event, error) {
	event, err := decodeReportPayload(kind, raw)
	if err != nil || kind == "progress" {
		return event, err
	}
	if err = validateReportArtifactPath(event, capabilityDir); err != nil {
		return contract.Event{}, err
	}
	value := event.Artifact
	if value.Size > 1024*1024 {
		return contract.Event{}, CodeError("report_artifact_too_large")
	}
	canonical, err := filepath.EvalSymlinks(value.Path)
	if err != nil {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	canonicalRoot, err := filepath.EvalSymlinks(capabilityDir)
	if err != nil {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	relative, err := filepath.Rel(capabilityDir, value.Path)
	if err != nil || filepath.Join(canonicalRoot, relative) != canonical {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	root, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	defer root.Close()
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() != value.Size {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	if info.Size() > 1024*1024 {
		return contract.Event{}, CodeError("report_artifact_too_large")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 1024*1024+1))
	after, statErr := file.Stat()
	afterStat, ok := infoSyscall(after)
	if err != nil || statErr != nil || !ok || n != value.Size || after.Size() != info.Size() || after.Mode() != info.Mode() || afterStat.Mtimespec != stat.Mtimespec || afterStat.Ctimespec != stat.Ctimespec || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(value.SHA256) {
		return contract.Event{}, CodeError("report_artifact_hash_mismatch")
	}
	return event, nil
}

func decodeReportPayload(kind string, raw json.RawMessage) (contract.Event, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if kind == "progress" {
		var value struct {
			Status    string `json:"status"`
			Sequence  int64  `json:"sequence"`
			ElapsedMS int64  `json:"elapsed_ms"`
		}
		if decoder.Decode(&value) != nil || (value.Status != "resource_sample" && value.Status != "checkpoint") || value.Sequence < 0 || value.ElapsedMS < 0 {
			return contract.Event{}, CodeError("invalid_report_payload")
		}
		return contract.Event{}, nil
	}
	var value struct {
		Status           string               `json:"status"`
		QuestionID       string               `json:"question_id,omitempty"`
		QuestionRevision int                  `json:"question_revision,omitempty"`
		QuestionKind     string               `json:"question_kind,omitempty"`
		Artifact         contract.ArtifactRef `json:"artifact"`
	}
	if decoder.Decode(&value) != nil {
		return contract.Event{}, CodeError("invalid_report_payload")
	}
	allowed := (kind == "result" && (value.Status == "completed" || value.Status == "partial")) || (kind == "failure" && (value.Status == "failed" || value.Status == "blocked")) || (kind == "question" && value.Status == "waiting_user" && value.QuestionID != "" && value.QuestionRevision > 0 && value.QuestionKind == "technical")
	if !allowed || value.Artifact.Path == "" || !filepath.IsAbs(value.Artifact.Path) || !validDigest(value.Artifact.SHA256) || value.Artifact.Size < 0 {
		return contract.Event{}, CodeError("invalid_report_payload")
	}
	return contract.Event{Artifact: &value.Artifact, QuestionID: value.QuestionID, QuestionRevision: value.QuestionRevision, QuestionKind: value.QuestionKind}, nil
}

func validateReportArtifactPath(event contract.Event, capabilityDir string) error {
	if event.Artifact == nil {
		return CodeError("invalid_report_payload")
	}
	rel, err := filepath.Rel(capabilityDir, event.Artifact.Path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return CodeError("report_artifact_outside_capability")
	}
	return nil
}
