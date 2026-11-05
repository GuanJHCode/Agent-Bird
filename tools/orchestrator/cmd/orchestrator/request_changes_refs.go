package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

func readOwnerChangeInput(path string, target any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return codeError("path_not_absolute")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return codeError("untrusted_private_file")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !owned(st) || st.Size() > adapter.ReworkBodyMaxBytes+8*1024 {
		return codeError("request_changes_utf8_bytes_exceeded")
	}
	data, e := io.ReadAll(io.LimitReader(f, adapter.ReworkBodyMaxBytes+8*1024+1))
	if e != nil {
		return e
	}
	if len(data) > adapter.ReworkBodyMaxBytes+8*1024 {
		return codeError("request_changes_utf8_bytes_exceeded")
	}
	return decode(data, target)
}
func ownerRefsBody(cap controlCapability, b ownerChangeBrief) []byte {
	raw, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: cap.ControllerThread, RunID: cap.RunID, TaskID: b.TaskID, WorkRevision: b.WorkRevision, Feedback: b.Feedback, Acceptance: b.Acceptance})
	return raw
}
func requireRefsCoordinator(ctx context.Context, state string) error {
	ready, e := ensureServer(ctx, state)
	if e != nil {
		return e
	}
	caps, _ := ready["capabilities"].([]string)
	if !slices.Contains(caps, "owner_rework_refs_v3") {
		return codeError("coordinator_upgrade_required")
	}
	return nil
}
func callRequestChangesRefs(ctx context.Context, state string, cap controlCapability, spec store.OwnerReworkSpec, body []byte, out io.Writer) error {
	if _, e := adapter.DecodeReworkBody(body); e != nil {
		return codeError("request_changes_utf8_bytes_exceeded")
	}
	hash := dispatchHash(string(body))
	rpc := func(req store.ReworkRefsRequest) (store.ReworkUploadReceipt, error) {
		req.BodySHA256 = hash
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		resp, e := call(callCtx, state, ipc.KindRequestChanges, coordinator.RequestChangesRequest{OwnerReworkSpec: spec, ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken, RefsV3: &req})
		if e != nil {
			var rejected remoteError
			if errors.As(e, &rejected) {
				return store.ReworkUploadReceipt{}, e
			}
			return store.ReworkUploadReceipt{}, codeError("request_changes_uncertain")
		}
		var result store.ReworkUploadReceipt
		if e = decode(resp.Payload, &result); e != nil {
			return result, codeError("request_changes_uncertain")
		}
		return result, nil
	}
	r, e := rpc(store.ReworkRefsRequest{Phase: "begin", BodySize: int64(len(body))})
	if e != nil {
		return e
	}
	if r.Receipt == nil {
		if r.UploadID == "" || r.WrittenBytes < 0 || r.WrittenBytes > int64(len(body)) {
			return codeError("request_changes_uncertain")
		}
		uploadID := r.UploadID
		for offset := r.WrittenBytes; offset < int64(len(body)); {
			end := offset + adapter.ReworkChunkMaxBytes
			if end > int64(len(body)) {
				end = int64(len(body))
			}
			r, e = rpc(store.ReworkRefsRequest{Phase: "chunk", UploadID: uploadID, Offset: offset, Data: body[offset:end]})
			if e != nil {
				return e
			}
			if r.WrittenBytes != end {
				return codeError("request_changes_uncertain")
			}
			offset = end
		}
		r, e = rpc(store.ReworkRefsRequest{Phase: "commit", UploadID: uploadID})
		if e != nil {
			return e
		}
	}
	if r.Receipt == nil {
		return codeError("request_changes_uncertain")
	}
	raw, e := json.Marshal(r.Receipt)
	if e != nil {
		return e
	}
	_, e = out.Write(append(raw, '\n'))
	return e
}
