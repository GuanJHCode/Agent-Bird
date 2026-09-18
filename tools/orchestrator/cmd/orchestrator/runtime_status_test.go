package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeStatusDoesNotCreateMissingDatabase(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "absent")
	err := run(context.Background(), []string{"runtime-status", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "runtime_unavailable" {
		t.Fatalf("%v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("read-only status created runtime")
	}
}

func TestRuntimeStatusRunObservation(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`CREATE TABLE segment_runtime(status TEXT)`, `CREATE TABLE stop_runtime(status TEXT)`, `CREATE TABLE runs(id TEXT)`, `CREATE TABLE tasks(id TEXT,run_id TEXT,status TEXT)`, `CREATE TABLE attempts(task_id TEXT)`, `INSERT INTO runs VALUES('existing')`, `INSERT INTO tasks VALUES('t','existing','ready')`, `CREATE TABLE host_launches(status TEXT)`, `CREATE TABLE runtime_hosts(status TEXT)`, `CREATE TABLE runtime_events(delivery_status TEXT)`, `CREATE TABLE report_events(delivery_status TEXT)`, `INSERT INTO host_launches VALUES('reserved')`, `INSERT INTO attempts VALUES('t')`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Chmod(filepath.Join(root, "state.db"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"existing", "absent", "' OR 1=1 --"} {
		var out bytes.Buffer
		if err = runtimeStatus(context.Background(), []string{"--state-dir", root, "--run-id", id}, &out); err != nil {
			t.Fatal(err)
		}
		var got struct {
			HostStates map[string]int `json:"host_states"`
			TaskStates map[string]int `json:"task_states"`
			Queued     int            `json:"queued_tasks"`
			Hosts      int            `json:"unreleased_hosts"`
			Runs       int            `json:"matching_runs"`
			Tasks      int            `json:"matching_tasks"`
			Attempts   int            `json:"matching_attempts"`
		}
		if err = json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.HostStates["reserved"] != 1 || got.TaskStates["ready"] != 1 {
			t.Fatalf("missing lifecycle states: %+v", got)
		}
		if got.Queued != 1 || got.Hosts != 1 {
			t.Fatalf("missing pre-segment work: %+v", got)
		}
		want := 0
		if id == "existing" {
			want = 1
		}
		if got.Runs != want || got.Tasks != want || got.Attempts != want {
			t.Fatalf("%s: %+v", id, got)
		}
	}
}
