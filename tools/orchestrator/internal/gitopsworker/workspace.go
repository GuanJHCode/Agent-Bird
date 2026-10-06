package gitopsworker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"io"
	"os"
	"syscall"
)

func (j *durableJournal) readBoundedRecord(ctx context.Context, kind string, value any) (bool, error) {
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	owner, owned := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !owned || owner.Uid != uint32(os.Geteuid()) || st.Size() > 16*1024*1024 {
		return false, errors.New("gitops_journal_unsafe")
	}
	dec := json.NewDecoder(io.LimitReader(f, 16*1024*1024+1))
	dec.DisallowUnknownFields()
	var body []byte
	for {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		var r struct {
			Version int             `json:"version"`
			Kind    string          `json:"kind"`
			Value   json.RawMessage `json:"value"`
		}
		err = dec.Decode(&r)
		if err == io.EOF {
			break
		}
		if err != nil || r.Version != 1 {
			return false, errors.New("gitops_journal_invalid")
		}
		if r.Kind != kind {
			continue
		}
		if len(r.Value) > 1024*1024 {
			return false, errors.New("gitops_journal_limit")
		}
		if body != nil && string(body) != string(r.Value) {
			return false, errors.New("gitops_journal_binding_conflict")
		}
		body = append([]byte(nil), r.Value...)
	}
	if body == nil {
		return false, nil
	}
	if err = json.Unmarshal(body, value); err != nil {
		return false, err
	}
	return true, nil
}

func (j *durableJournal) LoadWorkspacePrepared(ctx context.Context) (gitops.PreparedWorkspace, error) {
	var p gitops.PreparedWorkspace
	found, err := j.readBoundedRecord(ctx, "workspace_prepared", &p)
	if err != nil {
		return p, err
	}
	if !found {
		return p, errors.New("workspace_prepared_missing")
	}
	return p, nil
}

func (j *durableJournal) LoadCandidateFreeze(ctx context.Context) (gitops.CandidateReceipt, bool, error) {
	var c gitops.CandidateReceipt
	found, err := j.readBoundedRecord(ctx, "candidate_receipt", &c)
	return c, found, err
}
