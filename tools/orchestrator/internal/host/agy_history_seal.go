package host

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const agySealName = "agy-session-seal.json"
const agyTrajectoryLimit = 16 * 1024 * 1024

const agyProtobufStorage = "protobuf-v1"
const agySQLiteStorage = "sqlite-v1"

type agyHistorySeal struct {
	Version               int                      `json:"version"`
	StorageKind           string                   `json:"storage_kind,omitempty"`
	Origin                continuationOrigin       `json:"origin"`
	SegmentID             string                   `json:"segment_id"`
	ProducerID            string                   `json:"producer_id"`
	ExecutionEpoch        uint64                   `json:"execution_epoch"`
	TerminalKind          string                   `json:"terminal_kind"`
	SessionID             string                   `json:"session_id"`
	Process               contract.ProcessIdentity `json:"process"`
	HistoryPath           string                   `json:"history_path"`
	HistoryIdentity       continuationFileIdentity `json:"history_identity"`
	ConversationsIdentity continuationFileIdentity `json:"conversations_identity"`
	TrajectoryPath        string                   `json:"trajectory_path"`
	TrajectoryIdentity    continuationFileIdentity `json:"trajectory_identity"`
	Size                  int64                    `json:"size"`
	SHA256                string                   `json:"sha256"`
}

// Only callers after the original Handle's positive tree proof may capture.
// It does no Wait, Stop or process probing, and grants no new cleanup window.
func (h *Host) captureAGYHistorySeal(g contract.LaunchCommand, identity *process.Identity, kind string) error {
	if identity == nil || identity.PID < 1 || identity.PGID < 1 || identity.Birth == "" || (kind != contract.EventQuestion && kind != contract.EventStopped) {
		return errors.New("agy_history_seal_process_unproved")
	}
	original, err := h.originalContinuationSegment(g)
	if err != nil {
		return err
	}
	var history agyHistoryBinding
	if err = readContinuationMetadata(filepath.Join(h.spoolRoot, g.AttemptID, original, "provider-history.json"), &history); err != nil {
		return errors.New("agy_history_seal_binding_missing")
	}
	sourceGrant := g
	sourceGrant.SegmentID = original
	if history.Version != 1 || history.Origin != h.originForGrant(sourceGrant, "antigravity-cli") || history.Pin.Version != agyHistoryVersion || history.Pin.SHA256 != agyHistorySHA256 {
		return errors.New("agy_history_seal_binding_changed")
	}
	rootID, err := continuationDirectoryIdentity(history.Path, 0700)
	if err != nil || rootID != history.Identity {
		return errors.New("agy_history_seal_binding_changed")
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		return err
	}
	events, err := spool.Read()
	if err != nil {
		return err
	}
	processID := contract.ProcessIdentity{PID: identity.PID, PGID: identity.PGID, Birth: identity.Birth}
	session := ""
	for _, e := range events {
		if e.Kind == contract.EventUnknown {
			return errors.New("agy_history_seal_source_unknown")
		}
		if e.Kind != contract.EventSession {
			continue
		}
		if e.ProducerID != h.producerID || e.RunID != g.RunID || e.TaskID != g.TaskID || e.AttemptID != g.AttemptID || e.SegmentID != g.SegmentID || e.WorkRevision != g.WorkRevision || e.ExecutionEpoch != g.ExecutionEpoch || e.SessionKind != "conversation-id" || e.SessionID == "" || e.Process == nil || *e.Process != processID || (session != "" && session != e.SessionID) {
			return errors.New("agy_history_seal_session_changed")
		}
		session = e.SessionID
	}
	if session == "" || len(session) > 256 || strings.ContainsAny(session, "/\\\x00\r\n") || (g.Continuation != nil && session != g.SessionID) {
		return errors.New("agy_history_seal_session_missing")
	}
	conversations := filepath.Join(history.Path, "conversations")
	conversationsID, err := continuationDirectoryIdentity(conversations, 0)
	if err != nil {
		return errors.New("agy_history_seal_trajectory_missing")
	}
	kindOfStorage, file, err := agyTrajectorySnapshot(conversations, session, "", agyFileSnapshotChecked)
	if err != nil {
		return errors.New("agy_history_seal_trajectory_untrusted")
	}
	path := agyTrajectoryPath(conversations, session, kindOfStorage)
	seal := agyHistorySeal{Version: 2, StorageKind: kindOfStorage, Origin: history.Origin, SegmentID: g.SegmentID, ProducerID: h.producerID,
		ExecutionEpoch: g.ExecutionEpoch, TerminalKind: kind, SessionID: session, Process: processID,
		HistoryPath: history.Path, HistoryIdentity: history.Identity, ConversationsIdentity: conversationsID,
		TrajectoryPath: path, TrajectoryIdentity: file.Identity, Size: file.Size, SHA256: file.SHA256}
	return writeAGYHistorySeal(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID), seal)
}

// The private stage is exclusive. A partial write stays at the stage name and
// blocks recapture; it is never interpreted as a successful final receipt.
// Link publishes without replacing an existing receipt, then removes the stage
// so the final object has exactly one link. Neither existing nor partial seals
// are repaired during resume.
func writeAGYHistorySeal(dir string, seal agyHistorySeal) error {
	if err := continuationOwnedDirectory(dir, 0700); err != nil {
		return err
	}
	body, err := json.Marshal(seal)
	if err != nil || len(body) > 16*1024 {
		return errors.New("agy_history_seal_invalid")
	}
	body = append(body, '\n')
	stage, final := filepath.Join(dir, "."+agySealName+".tmp"), filepath.Join(dir, agySealName)
	f, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return errors.New("agy_history_seal_partial_or_exists")
	}
	n, err := f.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("agy_history_seal_partial")
	}
	if err = os.Link(stage, final); err != nil {
		return errors.New("agy_history_seal_partial_or_exists")
	}
	if err = os.Remove(stage); err != nil {
		return errors.New("agy_history_seal_partial")
	}
	dirFile, err := os.Open(dir)
	if err == nil {
		err = dirFile.Sync()
		if closeErr := dirFile.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = os.Remove(final)
		return errors.New("agy_history_seal_not_durable")
	}
	return nil
}

func (h *Host) validateAGYHistorySeal(g contract.LaunchCommand, history agyHistoryBinding) error {
	if err := h.validateTrustedContinuation(g); err != nil {
		return err
	}
	c := g.Continuation
	var seal agyHistorySeal
	if err := readContinuationMetadata(filepath.Join(h.spoolRoot, g.AttemptID, c.SourceSegmentID, agySealName), &seal); err != nil {
		return errors.New("agy_history_seal_missing_or_invalid")
	}
	kind := contract.EventQuestion
	if c.Mode == contract.ContinuationInterrupted || c.SourceKind == "report" {
		kind = contract.EventStopped
	}
	storageKind := seal.StorageKind
	if seal.Version == 1 && storageKind == "" {
		storageKind = agyProtobufStorage
	} else if seal.Version != 2 || (storageKind != agyProtobufStorage && storageKind != agySQLiteStorage) {
		return errors.New("agy_history_seal_binding_changed")
	}
	if seal.Origin != history.Origin || seal.SegmentID != c.SourceSegmentID || seal.ProducerID != h.producerID || seal.TerminalKind != kind || seal.SessionID != g.SessionID || seal.Process.PID < 1 || seal.Process.PGID < 1 || seal.Process.Birth == "" || seal.HistoryPath != history.Path || seal.HistoryIdentity != history.Identity {
		return errors.New("agy_history_seal_binding_changed")
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: c.SourceSegmentID})
	if err != nil {
		return err
	}
	events, err := spool.Read()
	if err != nil {
		return err
	}
	source, exited, session := false, false, false
	for _, e := range events {
		if e.ExecutionEpoch != seal.ExecutionEpoch || e.Process == nil || *e.Process != seal.Process {
			continue
		}
		session = session || e.Kind == contract.EventSession && e.SessionKind == "conversation-id" && e.SessionID == seal.SessionID
		exited = exited || e.Kind == contract.EventExited
		if c.SourceKind == "host" {
			source = source || e.EventID == c.SourceEventID && e.Kind == kind && e.Sequence == c.SourceEventRevision && e.PayloadHash == c.SourceEventSHA256
		} else {
			// The existing trusted report-source guard authenticates its exact
			// external event; the Host seal must still match the original Stop.
			source = source || e.Kind == contract.EventStopped
		}
	}
	if !session || !source || !exited {
		return errors.New("agy_history_seal_source_changed")
	}
	conversations := filepath.Join(history.Path, "conversations")
	identity, err := continuationDirectoryIdentity(conversations, 0)
	path := agyTrajectoryPath(conversations, g.SessionID, storageKind)
	if err != nil || identity != seal.ConversationsIdentity || path != seal.TrajectoryPath {
		return errors.New("agy_history_seal_trajectory_changed")
	}
	_, file, err := agyTrajectorySnapshot(conversations, g.SessionID, storageKind, agyFileSnapshotChecked)
	if err != nil || file.Missing || file.Identity != seal.TrajectoryIdentity || file.Size != seal.Size || file.SHA256 != seal.SHA256 {
		return errors.New("agy_history_seal_trajectory_changed")
	}
	return nil
}

func agyTrajectoryPath(conversations, session, kind string) string {
	suffix := ".pb"
	if kind == agySQLiteStorage {
		suffix = ".db"
	}
	return filepath.Join(conversations, session+suffix)
}

// Lstat occupants, including dangling links and empty sidecars, are not absence.
// Decline mixed histories even if the native SQLite manager would prefer .db.
func agyTrajectoryState(conversations, session, expected string) (string, error) {
	absent := func(path string) (bool, error) {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	pbMissing, pbErr := absent(agyTrajectoryPath(conversations, session, agyProtobufStorage))
	dbPath := agyTrajectoryPath(conversations, session, agySQLiteStorage)
	dbMissing, dbErr := absent(dbPath)
	if pbErr != nil || dbErr != nil || pbMissing == dbMissing {
		return "", errors.New("agy_history_backend_ambiguous")
	}
	kind := agyProtobufStorage
	if !dbMissing {
		kind = agySQLiteStorage
	}
	if expected != "" && kind != expected {
		return "", errors.New("agy_history_backend_changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		missing, err := absent(dbPath + suffix)
		if err != nil || !missing {
			return "", errors.New("agy_history_sidecar_present_or_untrusted")
		}
	}
	return kind, nil
}

// The production caller always supplies the real bounded same-fd reader.
// The private parameter allows pure tests to interleave real file operations
// after that read and prove the post-read alternate/sidecar fence is required.
func agyTrajectorySnapshot(conversations, session, expected string, read func(string, string, int64, bool) (agyConfigReference, error)) (string, agyConfigReference, error) {
	kind, err := agyTrajectoryState(conversations, session, expected)
	if err != nil {
		return "", agyConfigReference{}, err
	}
	path := agyTrajectoryPath(conversations, session, kind)
	before, err := os.Lstat(path)
	if err != nil {
		return "", agyConfigReference{}, err
	}
	file, err := read(path, filepath.Base(path), agyTrajectoryLimit, kind == agySQLiteStorage)
	if err != nil || file.Missing || file.Size < 1 {
		return "", file, errors.New("agy_history_trajectory_untrusted")
	}
	if _, err = agyTrajectoryState(conversations, session, kind); err != nil {
		return "", file, err
	}
	after, err := os.Lstat(path)
	owner, ok := before.Sys().(*syscall.Stat_t)
	if err != nil {
		return "", file, errors.New("agy_history_trajectory_changed")
	}
	afterOwner, afterOK := after.Sys().(*syscall.Stat_t)
	if !ok || !afterOK || afterOwner.Nlink != 1 || afterOwner.Uid != owner.Uid || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() || file.Size != before.Size() || file.Identity != (continuationFileIdentity{uint64(owner.Dev), owner.Ino, owner.Uid, uint32(before.Mode().Perm())}) {
		return "", file, errors.New("agy_history_trajectory_changed")
	}
	return kind, file, nil
}
