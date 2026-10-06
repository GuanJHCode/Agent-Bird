package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type coreCLIRecoveryRecord struct {
	Version           int    `json:"version"`
	ScopeSHA256       string `json:"scope_sha256"`
	Attempts          int    `json:"attempts"`
	NextAttemptUnixMS int64  `json:"next_attempt_unix_ms"`
	Unavailable       bool   `json:"unavailable"`
	Pending           bool   `json:"pending"`
	LastPID           int    `json:"last_pid"`
	LastBirth         string `json:"last_birth"`
}

func coreCLIReadRecovery(t *testing.T, bootstrap hostBootstrap) coreCLIRecoveryRecord {
	t.Helper()
	var record coreCLIRecoveryRecord
	data, err := os.ReadFile(filepath.Join(bootstrap.SpoolRoot, ".coordinator-recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

type coreCLIHostIdentity struct {
	PID    int
	Birth  string
	Epoch  uint64
	Status string
}

func coreCLIReadHostIdentity(t *testing.T, state, launch string) coreCLIHostIdentity {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db")}
	u.RawQuery = "mode=ro"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var identity coreCLIHostIdentity
	if err := db.QueryRowContext(ctx, `SELECT pid,birth,coordinator_epoch,status FROM runtime_hosts WHERE launch_id=?`, launch).Scan(&identity.PID, &identity.Birth, &identity.Epoch, &identity.Status); err != nil {
		t.Fatal(err)
	}
	return identity
}
