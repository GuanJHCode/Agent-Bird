package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
)

type reportCapabilityFile struct {
	Version        int    `json:"version"`
	SocketPath     string `json:"socket_path"`
	CapabilityID   string `json:"capability_id"`
	Token          string `json:"token"`
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	SegmentID      string `json:"segment_id"`
	ProducerID     string `json:"producer_id"`
	WorkRevision   int    `json:"work_revision"`
	ExecutionEpoch uint64 `json:"execution_epoch"`
}

func (h *Host) prepareReportCapability(ctx context.Context, cfg SourceConfig, grant contract.LaunchCommand, session *sourceSession) (string, error) {
	if grant.ReportCapabilityID == "" {
		return "", errors.New("report_capability_missing")
	}
	dir := filepath.Join(h.spoolRoot, ".reports", grant.ReportCapabilityID)
	if err := ensurePrivateReportDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "capability.json")
	var capability reportCapabilityFile
	if body, err := os.ReadFile(path); err == nil {
		info, statErr := os.Lstat(path)
		stat, ok := infoSyscall(info)
		if statErr != nil || !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
			return "", errors.New("report_capability_unsafe")
		}
		if decodePayload(body, &capability) != nil {
			return "", errors.New("report_capability_unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		tokenBytes := make([]byte, 32)
		if _, err = rand.Read(tokenBytes); err != nil {
			return "", err
		}
		capability = reportCapabilityFile{Version: 1, SocketPath: cfg.SocketPath, CapabilityID: grant.ReportCapabilityID, Token: "report-token-" + hex.EncodeToString(tokenBytes), RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, ProducerID: h.producerID, WorkRevision: grant.WorkRevision, ExecutionEpoch: session.epoch}
		if err = writeReportCapability(path, capability); err != nil {
			return "", err
		}
	}
	if capability.Version != 1 || capability.SocketPath != cfg.SocketPath || capability.CapabilityID != grant.ReportCapabilityID || capability.RunID != grant.RunID || capability.TaskID != grant.TaskID || capability.AttemptID != grant.AttemptID || capability.SegmentID != grant.SegmentID || capability.ProducerID != h.producerID || capability.WorkRevision != grant.WorkRevision || capability.Token == "" {
		return "", errors.New("report_capability_conflict")
	}
	capability.ExecutionEpoch = session.epoch
	registration := contract.ReportCapabilityRegistration{CapabilityID: capability.CapabilityID, TokenHash: hashText(capability.Token), CapabilityDir: dir, RunID: capability.RunID, TaskID: capability.TaskID, AttemptID: capability.AttemptID, SegmentID: capability.SegmentID, ProducerID: capability.ProducerID, WorkRevision: capability.WorkRevision, ExecutionEpoch: session.epoch}
	if err := session.registerReportCapability(ctx, registration); err != nil {
		return "", err
	}
	return path, nil
}

func infoSyscall(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func ensurePrivateReportDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("report_capability_dir_unsafe")
	}
	return nil
}

func writeReportCapability(path string, value reportCapabilityFile) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	temporary := path + ".new"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if err != nil {
		_ = os.Remove(temporary)
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

func (s *sourceSession) registerReportCapability(ctx context.Context, registration contract.ReportCapabilityRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := registration.CapabilityID
	payload, _ := json.Marshal(registration)
	ackChannel := make(chan frameResult, 1)
	s.ackMu.Lock()
	s.acks[requestID] = ackChannel
	s.ackMu.Unlock()
	defer func() { s.ackMu.Lock(); delete(s.acks, requestID); s.ackMu.Unlock() }()
	if err := ipc.Write(s.conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReportCapability, RequestID: requestID, Epoch: s.epoch, Payload: payload}); err != nil {
		return err
	}
	select {
	case frame := <-ackChannel:
		if frame.err != nil || frame.envelope.Kind != ipc.KindDurableAck || frame.envelope.RequestID != requestID {
			return errors.New("report_capability_ack_mismatch")
		}
		var ack contract.DurableAck
		if decodePayload(frame.envelope.Payload, &ack) != nil || ack.Status != "durable" || ack.EventID != requestID || ack.PayloadHash != registration.TokenHash {
			return errors.New("report_capability_ack_mismatch")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type reportInvocation struct {
	base             contract.InvocationView
	reportPath       string
	reportExecutable string
}

func (i reportInvocation) Args() []string           { return i.base.Args() }
func (i reportInvocation) WorkingDirectory() string { return i.base.WorkingDirectory() }
func (i reportInvocation) Stdin() []byte            { return i.base.Stdin() }
func (i reportInvocation) Environment() map[string]string {
	env := i.base.Environment()
	if env == nil {
		env = make(map[string]string)
	}
	env["ORCHESTRATOR_REPORT_EXECUTABLE"] = i.reportExecutable
	env["ORCHESTRATOR_REPORT_CAPABILITY"] = i.reportPath
	return env
}
func (i reportInvocation) OutputProvider() string {
	if value, ok := i.base.(interface{ OutputProvider() string }); ok {
		return value.OutputProvider()
	}
	return ""
}
