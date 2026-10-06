package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type continuationOrigin struct {
	Version              int    `json:"version"`
	RunID                string `json:"run_id"`
	TaskID               string `json:"task_id"`
	AttemptID            string `json:"attempt_id"`
	SegmentID            string `json:"segment_id"`
	WorkRevision         int    `json:"work_revision"`
	PlanRevision         int    `json:"plan_revision"`
	Provider             string `json:"provider"`
	AdapterPayloadSHA256 string `json:"adapter_payload_sha256"`
	OwnerContextID       string `json:"owner_context_id"`
}

// Bound exclusively from the source-owned bootstrap, never provider output.
func (h *Host) BindContinuationOwner(originContextID string) error {
	if originContextID == "" || len(originContextID) > 256 {
		return errors.New("continuation_owner_invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.continuationOwner != "" && h.continuationOwner != originContextID {
		return errors.New("continuation_owner_changed")
	}
	h.continuationOwner = originContextID
	return nil
}

func (h *Host) originForGrant(g contract.LaunchCommand, provider string) continuationOrigin {
	return continuationOrigin{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision, Provider: provider, AdapterPayloadSHA256: hashBytes(g.AdapterPayload), OwnerContextID: h.continuationOwner}
}

func (h *Host) recordContinuationOrigin(g contract.LaunchCommand, inv contract.InvocationView) error {
	var p adapter.InvocationPayload
	if json.Unmarshal(g.AdapterPayload, &p) != nil || p.Profile == nil || (p.Provider != "claude-code" && p.Provider != "antigravity-cli") || p.CandidateAction != nil {
		return nil
	}
	if g.Continuation != nil {
		return nil
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		return err
	}
	origin := h.originForGrant(g, p.Provider)
	var old continuationOrigin
	if err = readContinuationMetadata(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "continuation-origin.json"), &old); err == nil {
		if old != origin {
			return errors.New("continuation_origin_changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return spool.WriteMetadata("continuation-origin.json", origin)
}

func readContinuationOrigin(path string) (continuationOrigin, error) {
	var o continuationOrigin
	if err := readContinuationMetadata(path, &o); err != nil {
		return o, err
	}
	if o.Version != 1 || o.RunID == "" || o.TaskID == "" || o.AttemptID == "" || o.SegmentID == "" || o.WorkRevision < 1 || o.PlanRevision < 1 || len(o.AdapterPayloadSHA256) != 64 {
		return o, errors.New("continuation_origin_invalid")
	}
	return o, nil
}

func readContinuationMetadata(path string, out any) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 16*1024 || !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 {
		return errors.New("continuation_metadata_untrusted")
	}
	body, err := io.ReadAll(io.LimitReader(f, 16*1024+1))
	if err != nil {
		return err
	}
	if len(body) > 16*1024 {
		return errors.New("continuation_metadata_untrusted")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil {
		return errors.New("continuation_metadata_invalid")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("continuation_metadata_invalid")
	}
	return nil
}
