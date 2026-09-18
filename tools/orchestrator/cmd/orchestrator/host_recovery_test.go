package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Recovery must bring back the registered Host without accepting its result or
// releasing its dependent task, even when a newer CLI lives at another path.
func TestCLIRecoverHostPreservesPausedRun(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "recover-host-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// Optional real previous-release Host, exercised in the cross-version check.
	originalCLI := bin
	if old := os.Getenv("CBO_TEST_OLD_HOST"); old != "" {
		originalCLI, err = filepath.EvalSymlinks(old)
		if err != nil {
			t.Fatal(err)
		}
	}
	runBinary(t, ctx, bin, "ensure-running", "--state-dir", state)
	request := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(request, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"controller_thread": "owner", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, originalCLI, "owner-bind", "--state-dir", state, "--request", request), &owner); err != nil {
		t.Fatal(err)
	}
	ownerPath := owner["owner_capability"]
	delete(owner, "version")
	owner["run_id"], owner["plan_revision"], owner["delivery_mode"] = "run", 1, "collect"
	marker := filepath.Join(root, "must-not-run")
	owner["tasks"] = []any{
		map[string]any{"id": "first", "max_attempts": 3, "max_active_ms": 600000, "completion_policy": "owner_review", "adapter": map[string]any{"kind": "fake", "args": []string{"/bin/sh", "-c", "printf result"}, "directory": root}},
		map[string]any{"id": "next", "max_attempts": 3, "dependencies": []string{"first"}, "completion_policy": "owner_review", "adapter": map[string]any{"kind": "fake", "args": []string{"/usr/bin/touch", marker}, "directory": root}},
	}
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, originalCLI, "submit", "--state-dir", state, "--request", request), &submitted); err != nil {
		t.Fatal(err)
	}
	for {
		s := recoveryTaskStatus(t, ctx, bin, state, "first", submitted.ControlFile)
		if s.Status == "result_ready" && s.SegmentStatus == "exited" {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("not paused: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := recoveryTaskStatus(t, ctx, bin, state, "first", submitted.ControlFile)
	if live, err := process.Birth(before.HostPID); err != nil || live != before.HostBirth {
		t.Fatal("host identity changed")
	}
	p, err := os.FindProcess(before.HostPID)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Kill(); err != nil {
		t.Fatal(err)
	}
	for {
		s := recoveryTaskStatus(t, ctx, bin, state, "first", submitted.ControlFile)
		if s.HostStatus == "offline" {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("host not offline: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Use a different CLI path; the original Host executable must stay pinned.
	newCLI := filepath.Join(root, "new-cli")
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(newCLI, raw, 0700); err != nil {
		t.Fatal(err)
	}
	recoveryRequest := map[string]any{"version": 1, "task_id": "first", "work_revision": 1, "control_file": submitted.ControlFile, "owner_capability": ownerPath, "host_sha256": testFileSHA(t, originalCLI)}
	for _, tc := range []struct {
		field string
		value any
		want  string
	}{
		{"host_sha256", strings.Repeat("0", 64), "host_recovery_executable_mismatch"},
		{"work_revision", 2, "conflict"},
		{"owner_capability", filepath.Join(root, "absent-owner.json"), "owner_path_untrusted"},
	} {
		old := recoveryRequest[tc.field]
		recoveryRequest[tc.field] = tc.value
		write(recoveryRequest)
		recoveryError(t, ctx, newCLI, state, request, tc.want)
		recoveryRequest[tc.field] = old
	}
	write(recoveryRequest)
	beforeLedger := recoveryLedger(t, state)
	for i := 0; i < 2; i++ {
		runBinary(t, ctx, newCLI, "recover-host", "--state-dir", state, "--request", request)
	}
	after := recoveryTaskStatus(t, ctx, bin, state, "first", submitted.ControlFile)
	if after.HostStatus != "ready" || after.HostPID == before.HostPID || after.Status != before.Status || after.AttemptID != before.AttemptID || after.SegmentID != before.SegmentID || after.WorkRevision != before.WorkRevision {
		t.Fatalf("recovery changed work: before=%+v after=%+v", before, after)
	}
	t.Cleanup(func() {
		if b, err := process.Birth(after.HostPID); err == nil && b == after.HostBirth {
			p, _ := os.FindProcess(after.HostPID)
			_ = p.Kill()
		}
	})
	actual, err := process.ExecutablePath(after.HostPID)
	if err != nil || actual != originalCLI {
		t.Fatalf("original Host executable not used: %s %v", actual, err)
	}
	if s := recoveryTaskStatus(t, ctx, bin, state, "next", submitted.ControlFile); s.Status != "queued" || s.AttemptID != "" {
		t.Fatalf("recovery dispatched successor: %+v", s)
	}
	if recoveryLedger(t, state) != beforeLedger {
		t.Fatal("Host recovery changed business ledger")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dependent command ran")
	}
	// A second loss never treats an existing startup intent as permission to spawn.
	if b, err := process.Birth(after.HostPID); err != nil || b != after.HostBirth {
		t.Fatal("recovered Host identity changed")
	}
	restored, _ := os.FindProcess(after.HostPID)
	if err := restored.Kill(); err != nil {
		t.Fatal(err)
	}
	for recoveryTaskStatus(t, ctx, bin, state, "first", submitted.ControlFile).HostStatus != "offline" {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
	recoveryError(t, ctx, newCLI, state, request, "host_recovery_uncertain")
	if recoveryLedger(t, state) != beforeLedger {
		t.Fatal("uncertain recovery changed business ledger")
	}
}

func recoveryError(t *testing.T, ctx context.Context, bin, state, request, want string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, "recover-host", "--state-dir", state, "--request", request)
	output, err := cmd.CombinedOutput()
	var result struct {
		Error string `json:"error"`
	}
	if err == nil || json.Unmarshal(output, &result) != nil || result.Error != want {
		t.Fatalf("recover error=%s want=%s err=%v", output, want, err)
	}
}

func recoveryLedger(t *testing.T, state string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out strings.Builder
	for _, table := range []string{"tasks", "task_runtime", "attempts", "segments", "budget_runtime", "runtime_events", "review_decisions", "delivery_receipts"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			v := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range v {
				ptr[i] = &v[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(v)
			out.Write(raw)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out.String()
}

type recoveryStatus struct {
	Status        string `json:"status"`
	WorkRevision  int    `json:"work_revision"`
	AttemptID     string `json:"attempt_id"`
	SegmentID     string `json:"segment_id"`
	SegmentStatus string `json:"segment_status"`
	HostPID       int    `json:"host_pid"`
	HostBirth     string `json:"host_birth"`
	HostStatus    string `json:"host_status"`
}

func recoveryTaskStatus(t *testing.T, ctx context.Context, bin, state, task, control string) recoveryStatus {
	t.Helper()
	var s recoveryStatus
	if err := json.Unmarshal(runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", task, "--control-file", control), &s); err != nil {
		t.Fatal(err)
	}
	return s
}
