package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Never copy control/report capability files, auth homes or full environments.
// Preserve only this owned, no-account fixture's phase/events and tree probes.
func c04SaveFixtureFailure(t *testing.T, root, state string) {
	if !t.Failed() {
		return
	}
	evidence := os.Getenv("C04_TEST_EVIDENCE_DIR")
	if evidence == "" {
		t.Log("failure evidence directory unset")
		return
	}
	destination := filepath.Join(evidence, strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Log(err)
		return
	}
	groups := map[int]*contract.ProcessIdentity{}
	spool := filepath.Join(state, "host-spool")
	err := filepath.WalkDir(spool, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".reports" || entry.Name() == ".grants" || entry.Name() == "scratch" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if name != "launch.json" && name != "continuation-origin.json" && name != "provider-history.json" && name != "provider-session.json" && name != "provider-diagnostics.json" && !(strings.HasPrefix(name, "event-") && strings.HasSuffix(name, ".json")) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
			return nil
		}
		if name == "provider-diagnostics.json" {
			relative, err := filepath.Rel(spool, path)
			st, ok := info.Sys().(*syscall.Stat_t)
			if err != nil || len(strings.Split(relative, string(filepath.Separator))) != 4 || info.Mode().Perm() != 0600 || !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
				return nil
			}
		}
		var body []byte
		if name == "provider-diagnostics.json" {
			body, err = c04ReadFixtureDiagnostics(path, info)
		} else {
			body, err = os.ReadFile(path)
		}
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(target, body, 0600); err != nil {
			return err
		}
		var event contract.Event
		if json.Unmarshal(body, &event) == nil && event.Process != nil && event.Process.PGID > 0 {
			groups[event.Process.PGID] = event.Process
		}
		return nil
	})
	if err != nil {
		t.Logf("fixture evidence walk: %v", err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(state, "state.db"))
	if err == nil {
		defer raw.Close()
		rows, err := raw.Query(`SELECT json_object('task_id',a.task_id,'attempt_id',s.attempt_id,'segment_id',s.segment_id,'status',s.status,'outcome',s.outcome,'command_id',s.command_id,'host_id',s.host_id,'execution_epoch',s.execution_epoch,'granted_ms',b.granted_ms,'used_ms',b.used_ms,'budget_status',b.status,'grant_deadline_unix_ms',s.deadline_unix_ms,'grant_created_at',s.created_at,'stop_command_id',z.command_id,'stop_reason',z.reason,'stop_deadline_unix_ms',z.deadline_unix_ms,'stop_created_at',z.created_at,'stop_status',z.status) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN budget_runtime b ON b.id=s.reservation_id LEFT JOIN stop_runtime z ON z.segment_id=s.segment_id AND z.host_id=s.host_id ORDER BY s.created_at`)
		if err == nil {
			var records []json.RawMessage
			for rows.Next() {
				var body string
				if rows.Scan(&body) == nil {
					records = append(records, json.RawMessage(body))
				}
			}
			rows.Close()
			body, _ := json.MarshalIndent(records, "", "  ")
			os.WriteFile(filepath.Join(destination, "segments.json"), body, 0600)
		}
	}
	type observation struct {
		Identity     *contract.ProcessIdentity `json:"identity"`
		CurrentBirth string                    `json:"current_birth,omitempty"`
		BirthError   string                    `json:"birth_error,omitempty"`
		GroupProbe   string                    `json:"group_probe"`
		PS           string                    `json:"ps"`
		PSError      string                    `json:"ps_error,omitempty"`
	}
	var observed []observation
	for pgid, identity := range groups {
		row := observation{Identity: identity}
		birth, err := process.Birth(identity.PID)
		row.CurrentBirth = birth
		if err != nil {
			row.BirthError = fmt.Sprint(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		err = process.ConfirmGroupExitedContext(ctx, pgid)
		cancel()
		if err == nil {
			row.GroupProbe = "confirmed_gone"
		} else {
			row.GroupProbe = err.Error()
		}
		ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
		out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "pid=,ppid=,pgid=,state=,comm=", "-g", strconv.Itoa(pgid)).CombinedOutput()
		cancel()
		if len(out) > 16*1024 {
			out = out[:16*1024]
		}
		row.PS = string(out)
		if err != nil {
			row.PSError = err.Error()
		}
		observed = append(observed, row)
	}
	body, _ := json.MarshalIndent(observed, "", "  ")
	os.WriteFile(filepath.Join(destination, "worker-tree-observations.json"), body, 0600)
	t.Logf("C04 owned fixture failure evidence: %s", destination)
}

func c04ReadFixtureDiagnostics(path string, original os.FileInfo) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(original, info) || !info.Mode().IsRegular() || info.Size() != original.Size() {
		return nil, fmt.Errorf("fixture diagnostics identity changed")
	}
	body, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 || int64(len(body)) != info.Size() {
		return nil, fmt.Errorf("fixture diagnostics size changed")
	}
	return body, nil
}
