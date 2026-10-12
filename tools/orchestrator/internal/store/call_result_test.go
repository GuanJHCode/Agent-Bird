package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestReadCallResultRequiresOwnerExitAndCommittedArtifact(t *testing.T) {
	for _, kind := range []string{"success", "failure", "foreign_owner", "active", "unknown", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			f := newHostRetentionFixture(t, kind == "failure", false)
			owner := "controller"
			if kind == "foreign_owner" {
				owner = "foreign"
			}
			if kind == "active" || kind == "unknown" {
				status := "running"
				if kind == "unknown" {
					status = "unknown"
				}
				if _, err := f.db.sql.Exec(`UPDATE segments SET status=? WHERE id=?`, status, f.grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "tampered" {
				if err := os.WriteFile(f.proof.Path, []byte("different bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.db.ReadCallResult(context.Background(), f.grant.TaskID, owner, f.token, 0)
			if kind == "success" || kind == "failure" {
				if err != nil || !out.ProcessExited || out.EventKind == "" || out.PayloadSHA256 != f.event.PayloadHash || out.Content != "terminal retained body" || out.NextOffset != out.TotalBytes {
					t.Fatalf("%+v %v", out, err)
				}
			} else if err == nil {
				t.Fatalf("untrusted result accepted: %+v", out)
			}
		})
	}
}

func TestReadCallResultLaunchRejectedIsNeverStarted(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderAGY)
	f.send(t, f.event(contract.EventPrepared))
	e := rejectionEvent(t, f)
	f.send(t, e)
	out, err := f.db.ReadCallResult(context.Background(), f.grant.TaskID, "owner", f.receipt.ControlToken, 0)
	if err != nil || out.ProcessExited || out.EventKind != contract.EventLaunchRejected || out.PayloadSHA256 != e.PayloadHash || out.Content == "" || out.NextOffset != out.TotalBytes {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestReadCallResultPagesUTF8AndFitsIPC(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderAGY)
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
		f.send(t, f.event(kind))
	}
	body := []byte(strings.Repeat(`"界"`, 5000))
	ref := managedHostArtifactForTest(t, f.db, f.grant, "terminal", 0, body)
	result := f.event(contract.EventResult)
	result.Artifact, result.PayloadHash = ref, ref.SHA256
	f.send(t, result)
	exit := f.event(contract.EventExited)
	code := 0
	exit.Artifact, exit.ExitCode, exit.ActiveMS = ref, &code, 1
	f.send(t, exit)

	var rebuilt []byte
	for offset := 0; ; {
		out, err := f.db.ReadCallResult(context.Background(), f.grant.TaskID, "owner", f.receipt.ControlToken, offset)
		if err != nil || out.EventKind != contract.EventResult || !out.ProcessExited || out.TotalBytes != len(body) || len(out.Content) > callResultPageBytes || !utf8.ValidString(out.Content) {
			t.Fatalf("offset=%d result=%+v err=%v", offset, out, err)
		}
		encoded, err := json.Marshal(out)
		if err != nil || len(encoded) >= 64<<10 {
			t.Fatalf("response bytes=%d err=%v", len(encoded), err)
		}
		rebuilt = append(rebuilt, out.Content...)
		if out.NextOffset == out.TotalBytes {
			break
		}
		if out.NextOffset <= offset {
			t.Fatalf("cursor did not advance: %d -> %d", offset, out.NextOffset)
		}
		offset = out.NextOffset
	}
	if string(rebuilt) != string(body) {
		t.Fatal("page reconstruction changed content")
	}
	for _, offset := range []int{-1, len(body) + 1, 3} {
		if _, err := f.db.ReadCallResult(context.Background(), f.grant.TaskID, "owner", f.receipt.ControlToken, offset); err == nil {
			t.Fatalf("invalid offset accepted: %d", offset)
		}
	}
}
