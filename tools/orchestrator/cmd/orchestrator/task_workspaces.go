package main

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

// Only plan paths here. Host materialization remains the only creator, under
// the existing Git journal, immutable base and candidate scope checks.
func prepareTaskWorkspaces(handle string, tasks []coordinator.TaskRequest) error {
	for i := range tasks {
		raws := append([]json.RawMessage{tasks[i].AdapterPayload}, tasks[i].Fallbacks...)
		for j, raw := range raws {
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.CandidateWorkspace == nil && p.CandidateAction == nil {
				continue
			}
			if p.CandidateAction != nil && p.CandidateAction.Operation == "integrate" {
				continue
			}
			if p.Directory == "" {
				if p.CandidateWorkspace != nil {
					p.CandidateWorkspace.AutoDirectory = true
				}
				if p.CandidateAction != nil {
					p.CandidateAction.AutoDirectory = true
				}
				key := sha256.Sum256([]byte(handle + "\x00" + tasks[i].ID))
				p.Directory = filepath.Join(filepath.Dir(handle), "workspace-"+hex.EncodeToString(key[:12]))
				raws[j], err = json.Marshal(p)
				if err != nil {
					return err
				}
			}
		}
		tasks[i].AdapterPayload = raws[0]
		if len(raws) > 1 {
			tasks[i].Fallbacks = raws[1:]
		}
	}
	return nil
}
