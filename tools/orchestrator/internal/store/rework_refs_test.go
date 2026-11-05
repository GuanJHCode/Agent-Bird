package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func refsFixture(t *testing.T) (ownerReworkFixture, []byte) {
	t.Helper()
	f := newOwnerReworkFixture(t)
	f.hello.OwnerReworkRefsV3 = true
	if _, e := f.db.RegisterHost(context.Background(), f.hello, 1); e != nil {
		t.Fatal(e)
	}
	f.spec.Feedback = ""
	f.spec.Acceptance = ""
	data, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: 1, Feedback: strings.Repeat("修复<\"\n", 14000), Acceptance: "all original conditions"})
	return f, data
}
func beginRefs(t *testing.T, f ownerReworkFixture, data []byte) ReworkUploadReceipt {
	t.Helper()
	r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func uploadRefs(t *testing.T, f ownerReworkFixture, data []byte, id string) {
	t.Helper()
	for off := 0; off < len(data); {
		end := off + adapter.ReworkChunkMaxBytes
		if end > len(data) {
			end = len(data)
		}
		r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "chunk", UploadID: id, Offset: int64(off), Data: data[off:end]})
		if e != nil {
			t.Fatal(e)
		}
		if r.WrittenBytes != int64(end) {
			t.Fatalf("progress=%+v", r)
		}
		off = end
	}
}

func TestReworkRefsLongBodyQuotaPinAndPreciseReplay(t *testing.T) {
	f, data := refsFixture(t)
	if len(data) <= 64*1024 || len(data) > adapter.ReworkBodyMaxBytes {
		t.Fatalf("fixture bytes %d", len(data))
	}
	before := int64(0)
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	u := beginRefs(t, f, data)
	var charged int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&charged)
	if charged-before < int64(len(data))+8192 {
		t.Fatalf("reservation uncharged %d", charged-before)
	}
	uploadRefs(t, f, data, u.UploadID)
	r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID, BodySHA256: runtimeHash(string(data))})
	if e != nil || r.Receipt == nil {
		t.Fatalf("commit %+v %v", r, e)
	}
	var raw, saved string
	f.db.sql.QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id='edit'`).Scan(&raw)
	f.db.sql.QueryRow(`SELECT revision_payload FROM retry_decisions WHERE command_id=?`, f.spec.CommandID).Scan(&saved)
	if len(raw) > 16*1024 || strings.Contains(saved, "all original conditions") || strings.Contains(raw, "all original conditions") {
		t.Fatalf("inline text leaked adapter=%d decision=%d", len(raw), len(saved))
	}
	var pins int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM rework_pins`).Scan(&pins)
	if pins != 2 {
		t.Fatalf("pins=%d", pins)
	}
	f.db.sql.Exec(`UPDATE runtime_hosts SET owner_rework_refs_v3=0,status='unavailable'`)
	again, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))})
	if e != nil || again.Receipt == nil || again.Receipt.RevisionSHA256 != r.Receipt.RevisionSHA256 {
		t.Fatalf("lost committed replay %+v %v", again, e)
	}
	if _, e = f.db.ReworkRefs(context.Background(), "other", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e == nil {
		t.Fatal("cross-owner accepted")
	}
	changed := runtimeHash("changed")
	if _, e = f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: changed}); e == nil {
		t.Fatal("changed body replay accepted")
	}
}

func TestReworkRefsCrashCutsAndExactRecovery(t *testing.T) {
	for _, cut := range []string{"reserved", "published_link", "published_unrecorded", "before_decision_commit", "after_decision_commit"} {
		t.Run(cut, func(t *testing.T) {
			f, data := refsFixture(t)
			hit := false
			f.db.reworkFault = func(phase string) error {
				if phase == cut && cut == "reserved" && !hit {
					hit = true
					return errors.New("crash")
				}
				return nil
			}
			r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))})
			if e != nil && !hit {
				t.Fatal(e)
			}
			f.db.reworkFault = nil
			if r.UploadID == "" {
				r = beginRefs(t, f, data)
			}
			uploadRefs(t, f, data, r.UploadID)
			if cut != "reserved" {
				f.db.reworkFault = func(phase string) error {
					if phase == cut && !hit {
						hit = true
						return errors.New("crash")
					}
					return nil
				}
			}
			_, e = f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: r.UploadID})
			if e != nil && !hit {
				t.Fatal(e)
			}
			f.db.reworkFault = nil
			// Actual SQLite close/reopen establishes durable recovery, not a JSON mock.
			path := f.db.path
			if e = f.db.Close(); e != nil {
				t.Fatal(e)
			}
			db, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			f.db = db
			replay, e := db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))})
			if e != nil {
				t.Fatal(e)
			}
			if replay.Receipt == nil {
				replay, e = db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: replay.UploadID})
				if e != nil {
					t.Fatal(e)
				}
			}
			if replay.Receipt == nil || replay.Receipt.WorkRevision != 2 {
				t.Fatalf("no durable receipt %+v", replay)
			}
		})
	}
}

func TestReworkRefsQuotaBeforeFileAndAbortGC(t *testing.T) {
	f, data := refsFixture(t)
	f.db.runControlLimit = 1
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))}); e == nil {
		t.Fatal("quota accepted")
	}
	if _, e := os.Lstat(f.db.objectRoot()); !os.IsNotExist(e) {
		t.Fatalf("created before reservation %v", e)
	}
	f.db.runControlLimit = 1 << 30
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
		t.Fatal("GC active upload")
	}
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	// An identity replacement must retain accounting and cannot delete the impostor.
	var id string
	f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
	path := filepath.Join(f.db.objectRoot(), id+".stage")
	old := path + ".held"
	if e := os.Rename(path, old); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, []byte("impostor"), 0600)
	var before int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
		t.Fatal("GC deleted replaced identity")
	}
	var after int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
	if before != after {
		t.Fatal("failed deletion released quota")
	}
	os.Remove(path)
	os.Rename(old, path)
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	var objects int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM rework_objects`).Scan(&objects)
	if objects != 0 {
		t.Fatalf("retained objects %d", objects)
	}
}

func TestReworkRefsCommitAndAbortGCMutuallyExclusive(t *testing.T) {
	f, data := refsFixture(t)
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	var wg sync.WaitGroup
	start := make(chan struct{})
	var committed, aborted ReworkUploadReceipt
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		committed, _ = f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID})
	}()
	go func() {
		defer wg.Done()
		<-start
		aborted, _ = f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID})
	}()
	close(start)
	wg.Wait()
	if committed.Receipt != nil {
		if aborted.Receipt == nil {
			t.Fatalf("abort changed committed state %+v", aborted)
		}
	} else if aborted.Status != "abandoned" {
		t.Fatalf("no winner commit=%+v abort=%+v", committed, aborted)
	}
}

func TestReworkRefsTwoRevisionsPreserveBriefAndCandidateBase(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	second, e := f.db.ClaimReady(ctx, f.host, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := adapter.DecodeInvocationPayload(second.AdapterPayload)
	brief := p.CandidateWorkspace.Rework.RefsV3.Brief
	f.spec = commitOwnerNextCandidate(t, f, second)
	f.spec.Feedback = ""
	f.spec.Acceptance = ""
	data, _ = json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: 2, Feedback: "second complete feedback", Acceptance: "every prior condition still valid"})
	u = beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e = f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	third, e := f.db.ClaimReady(ctx, f.host, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	p, _ = adapter.DecodeInvocationPayload(third.AdapterPayload)
	if p.CandidateWorkspace.BaseOID != f.spec.CandidateOID || p.CandidateWorkspace.Rework.RefsV3.Brief != brief || len(p.CandidateWorkspace.Rework.RefsV3.Conditions) != 2 {
		t.Fatal("brief/base/conditions changed")
	}
	final := commitOwnerNextCandidate(t, f, third)
	final.Feedback = ""
	final.Acceptance = ""
	if _, e = f.db.ReworkRefs(ctx, "owner", final, ReworkRefsRequest{Phase: "begin", BodySize: 10, BodySHA256: strings.Repeat("a", 64)}); e == nil || e.Error() != "attempt_limit" {
		t.Fatalf("last attempt %v", e)
	}
	s, e := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if e != nil || s.Tasks[0].ReviewableResult == nil || s.Tasks[0].ReviewableResult.CandidateOID == "" {
		t.Fatalf("last candidate unreviewable %+v %v", s, e)
	}
	var objects int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM rework_objects`).Scan(&objects)
	if objects != 3 {
		t.Fatalf("objects duplicated: %d", objects)
	}
}

func TestReworkRefsHostCapabilityQueueClaimPending(t *testing.T) {
	for _, phase := range []string{"queue", "claim", "pending"} {
		t.Run(phase, func(t *testing.T) {
			f, data := refsFixture(t)
			ctx := context.Background()
			if phase == "queue" {
				f.db.sql.Exec(`UPDATE runtime_hosts SET owner_rework_refs_v3=0`)
				if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))}); e == nil || e.Error() != "host_upgrade_required" {
					t.Fatalf("queue old host %v", e)
				}
				return
			}
			u := beginRefs(t, f, data)
			uploadRefs(t, f, data, u.UploadID)
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
				t.Fatal(e)
			}
			if phase == "claim" {
				f.db.sql.Exec(`UPDATE runtime_hosts SET owner_rework_refs_v3=0`)
				if _, e := f.db.ClaimReady(ctx, f.host, 1, 2); e == nil {
					t.Fatal("old host claimed refs")
				}
				return
			}
			g, e := f.db.ClaimReady(ctx, f.host, 1, 2)
			if e != nil {
				t.Fatal(e)
			}
			old := f.hello
			old.OwnerReworkRefsV3 = false
			if _, e = f.db.RegisterHost(ctx, old, 1); e != nil {
				t.Fatal(e)
			}
			pending, e := f.db.PendingLaunches(ctx, f.host, 1)
			if e != nil || len(pending) != 0 {
				t.Fatalf("downgraded pending %+v %v", pending, e)
			}
			if _, e = f.db.RegisterHost(ctx, f.hello, 1); e != nil {
				t.Fatal(e)
			}
			pending, e = f.db.PendingLaunches(ctx, f.host, 1)
			if e != nil || len(pending) != 1 || pending[0].CommandID != g.CommandID {
				t.Fatalf("precise reconnect %+v %v", pending, e)
			}
		})
	}
}

func TestReworkRefsPartialChunkAndUnknownOrphan(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "unknown-identity"}[unknown], func(t *testing.T) {
			f, data := refsFixture(t)
			u := beginRefs(t, f, data)
			var id string
			f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
			path := filepath.Join(f.db.objectRoot(), id+".stage")
			if unknown {
				if e := os.WriteFile(path, data[:20], 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "chunk", UploadID: u.UploadID, Data: data[:100]}); e == nil || e.Error() != "rework_cleanup_identity_unknown" {
					t.Fatalf("unknown orphan adopted %v", e)
				}
				return
			}
			if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "chunk", UploadID: u.UploadID, Data: data[:100]}); e != nil {
				t.Fatal(e)
			}
			// Crash: a partially written second chunk has not advanced durable offset.
			file, e := os.OpenFile(path, os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			file.WriteAt(data[100:140], 100)
			file.Sync()
			file.Close()
			r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "chunk", UploadID: u.UploadID, Offset: 100, Data: data[100:200]})
			if e != nil || r.WrittenBytes != 200 {
				t.Fatalf("partial recovery %+v %v", r, e)
			}
			bad := append([]byte(nil), data[100:200]...)
			bad[0] ^= 1
			if _, e = f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "chunk", UploadID: u.UploadID, Offset: 100, Data: bad}); e == nil {
				t.Fatal("changed replay accepted")
			}
		})
	}
}

func TestReworkRefsOwnerSnapshotRecheckedBeforeEveryPhaseAndReplay(t *testing.T) {
	for _, phase := range []string{"begin", "chunk", "commit", "abort", "gc", "replay"} {
		t.Run(phase, func(t *testing.T) {
			f, data := refsFixture(t)
			ctx := context.Background()
			req := ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))}
			if phase != "begin" {
				u := beginRefs(t, f, data)
				req = ReworkRefsRequest{Phase: phase, UploadID: u.UploadID}
				if phase == "chunk" {
					req.Data = data[:100]
				}
				if phase == "commit" || phase == "replay" {
					uploadRefs(t, f, data, u.UploadID)
				}
				if phase == "gc" {
					if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
						t.Fatal(e)
					}
				}
				if phase == "replay" {
					if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
						t.Fatal(e)
					}
					req.Phase = "begin"
					req.BodySize = int64(len(data))
					req.BodySHA256 = runtimeHash(string(data))
				}
			}
			observed, e := f.db.TaskOwnerActionBinding(ctx, "edit", "owner", f.token)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.db.sql.Exec(`UPDATE runs SET origin_birth='rebound' WHERE id='run'`); e != nil {
				t.Fatal(e)
			}
			if _, e = f.db.ReworkRefs(WithOwnerActionBinding(ctx, observed), "owner", f.spec, req); e == nil || e.Error() != "owner_binding_changed" {
				t.Fatalf("stale %s %v", phase, e)
			}
		})
	}
}

func TestReworkRefsInputLimitRejectsBeforeRevisionAndCapabilityMutation(t *testing.T) {
	f, _ := refsFixture(t)
	ctx := context.Background()
	var raw string
	f.db.sql.QueryRow(`SELECT adapter_payload FROM task_runtime WHERE task_id='edit'`).Scan(&raw)
	p, _ := adapter.DecodeInvocationPayload([]byte(raw))
	p.Prompt = strings.Repeat("brief", 2000)
	encoded, _ := json.Marshal(p)
	if _, e := f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='edit'`, string(encoded)); e != nil {
		t.Fatal(e)
	}
	for revision := 1; revision <= 2; revision++ {
		data, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: revision, Feedback: strings.Repeat("A", 260000), Acceptance: "preserve all conditions"})
		u := beginRefs(t, f, data)
		uploadRefs(t, f, data, u.UploadID)
		r, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID})
		if revision == 1 {
			if e != nil || r.Receipt == nil {
				t.Fatalf("first %+v %v", r, e)
			}
			g, e := f.db.ClaimReady(ctx, f.host, 1, 2)
			if e != nil {
				t.Fatal(e)
			}
			f.spec = commitOwnerNextCandidate(t, f, g)
			f.spec.Feedback = ""
			f.spec.Acceptance = ""
			continue
		}
		if e == nil || e.Error() != "rework_input_too_large" {
			t.Fatalf("oversize commit %v", e)
		}
		var rev int
		var status string
		f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&rev)
		f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='edit'`).Scan(&status)
		if rev != 2 || status != "result_ready" {
			t.Fatalf("oversize advanced rev=%d status=%s", rev, status)
		}
	}
}

func TestReworkRefsGCWaitsForPublishedDecisionWriter(t *testing.T) {
	f, data := refsFixture(t)
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	published, release := make(chan struct{}), make(chan struct{})
	f.db.reworkFault = func(phase string) error {
		if phase == "before_decision_commit" {
			close(published)
			<-release
		}
		return nil
	}
	result := make(chan error, 2)
	go func() {
		_, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID})
		result <- e
	}()
	<-published
	started := make(chan struct{})
	go func() {
		close(started)
		r, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID})
		if e == nil && r.Receipt == nil {
			e = errors.New("GC deleted committed objects")
		}
		result <- e
	}()
	<-started
	close(release)
	for i := 0; i < 2; i++ {
		if e := <-result; e != nil {
			t.Fatal(e)
		}
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM rework_objects WHERE status='pinned'`).Scan(&count)
	if count != 2 {
		t.Fatalf("GC raced publication count=%d", count)
	}
}

func TestReworkRefsNewCommandAfterAbortReusesOnlyImmutableBrief(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	first := beginRefs(t, f, data)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: first.UploadID}); e != nil {
		t.Fatal(e)
	}
	oldSpec := f.spec
	f.spec.CommandID = "replacement-command"
	second := beginRefs(t, f, data)
	// Old aborted upload GC may reclaim its own condition, never the brief now
	// protected by the new active upload's recovery reference.
	if _, e := f.db.ReworkRefs(ctx, "owner", oldSpec, ReworkRefsRequest{Phase: "gc", UploadID: first.UploadID}); e != nil {
		t.Fatal(e)
	}
	uploadRefs(t, f, data, second.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: second.UploadID}); e != nil {
		t.Fatal(e)
	}
	var count int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM rework_objects WHERE kind='brief'`).Scan(&count)
	if count != 1 {
		t.Fatalf("brief count %d", count)
	}
}

func TestReworkRefsTombstoneMetadataRemainsChargedAndRepeatedGCIsStable(t *testing.T) {
	f, _ := refsFixture(t)
	ctx := context.Background()
	data, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: 1, Feedback: "fix", Acceptance: "test"})
	var before int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	var reserved int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&reserved)
	var bodies int64
	f.db.sql.QueryRow(`SELECT SUM(size+4096) FROM rework_objects`).Scan(&bodies)
	if reserved-before != bodies+4096 {
		t.Fatalf("metadata reservation got=%d body=%d", reserved-before, bodies)
	}
	for _, phase := range []string{"abort", "gc", "gc"} {
		if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: phase, UploadID: u.UploadID}); e != nil {
			t.Fatal(e)
		}
	}
	var after int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
	if after-before != 4096 {
		t.Fatalf("tombstone released or doublecharged %d", after-before)
	}
}

func TestReworkRefsSummaryCapabilityMatchesV3Mutation(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	g, e := f.db.ClaimReady(ctx, f.host, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	f.spec = commitOwnerNextCandidate(t, f, g)
	f.spec.Feedback = ""
	f.spec.Acceptance = ""
	if _, e = f.db.sql.Exec(`UPDATE runtime_hosts SET owner_rework_refs_v3=0`); e != nil {
		t.Fatal(e)
	}
	s, e := f.db.SummarizeRun(ctx, "edit", "owner", f.token)
	if e != nil || s.Tasks[0].RequestChangesBlockedReason != "host_upgrade_required" {
		t.Fatalf("stale action projection %+v %v", s, e)
	}
}

func TestReworkRefsInvalidBodyNeverCommitsOrReleasesReservation(t *testing.T) {
	for _, kind := range []string{"missing", "hash", "truncated", "owner", "mode"} {
		t.Run(kind, func(t *testing.T) {
			f, data := refsFixture(t)
			if kind == "owner" {
				var b adapter.ReworkBody
				json.Unmarshal(data, &b)
				b.Owner = "other-owner"
				data, _ = json.Marshal(b)
			}
			u := beginRefs(t, f, data)
			uploadRefs(t, f, data, u.UploadID)
			var id string
			f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
			path := filepath.Join(f.db.objectRoot(), id+".stage")
			switch kind {
			case "missing":
				os.Remove(path)
			case "hash":
				file, _ := os.OpenFile(path, os.O_WRONLY, 0)
				file.WriteAt([]byte("!"), 20)
				file.Close()
			case "truncated":
				os.Truncate(path, int64(len(data)-1))
			case "mode":
				os.Chmod(path, 0644)
			}
			var before int64
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
			if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e == nil {
				t.Fatal("invalid body committed")
			}
			var decisions, revision int
			var after int64
			f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&decisions)
			f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&revision)
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
			if decisions != 0 || revision != 1 || after != before {
				t.Fatalf("failure mutated state decisions=%d rev=%d quota=%d/%d", decisions, revision, before, after)
			}
		})
	}
}

func TestReworkRefsNeverGuessesLegacyReworkedBrief(t *testing.T) {
	f := newOwnerReworkFixture(t)
	ctx := context.Background()
	if _, e := f.db.QueueRequestChanges(ctx, f.spec); e != nil {
		t.Fatal(e)
	}
	g, e := f.db.ClaimReady(ctx, f.host, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	f.spec = commitOwnerNextCandidate(t, f, g)
	f.spec.Feedback = ""
	f.spec.Acceptance = ""
	f.hello.OwnerReworkRefsV3 = true
	if _, e = f.db.RegisterHost(ctx, f.hello, 1); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: 2, Feedback: "fix", Acceptance: "preserve"})
	if _, e = f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))}); e == nil || e.Error() != "rework_legacy_upgrade_unsupported" {
		t.Fatalf("guessed old prompt %v", e)
	}
}

func TestReworkRefsOriginalEventAckReplaysAndRejectsChangedTuple(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-launch", true: "after-launch"}[claim], func(t *testing.T) {
			f, data := refsFixture(t)
			ctx := context.Background()
			u := beginRefs(t, f, data)
			uploadRefs(t, f, data, u.UploadID)
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e != nil {
				t.Fatal(e)
			}
			if claim {
				if _, e := f.db.ClaimReady(ctx, f.host, 1, 2); e != nil {
					t.Fatal(e)
				}
			}
			ack := AckDecision{EventID: f.event.EventID, EventRevision: f.event.EventRevision, EventHash: f.event.PayloadHash, ActionSlot: f.event.ActionSlot, Decision: "handled", CommandID: "ack-v3"}
			for i := 0; i < 2; i++ {
				if e := f.db.AckDecision(ctx, "edit", ack); e != nil {
					t.Fatalf("exact ACK %v", e)
				}
			}
			ack.EventHash = runtimeHash("changed")
			if e := f.db.AckDecision(ctx, "edit", ack); e == nil {
				t.Fatal("changed old revision ACK accepted")
			}
		})
	}
}

func TestReworkRefsNoContextHighRevisionCannotBecomeOriginalBrief(t *testing.T) {
	f := newOwnerReworkArtifactRevisionFixture(t, nil, nil, 3)
	f.hello.OwnerReworkRefsV3 = true
	if _, e := f.db.RegisterHost(context.Background(), f.hello, 1); e != nil {
		t.Fatal(e)
	}
	f.spec.Feedback = ""
	f.spec.Acceptance = ""
	data, _ := json.Marshal(adapter.ReworkBody{Version: 3, Kind: "condition", Owner: "owner", RunID: "run", TaskID: "edit", WorkRevision: 3, Feedback: "fix", Acceptance: "test"})
	if _, e := f.db.ReworkRefs(context.Background(), "owner", f.spec, ReworkRefsRequest{Phase: "begin", BodySize: int64(len(data)), BodySHA256: runtimeHash(string(data))}); e == nil || e.Error() != "rework_legacy_upgrade_unsupported" {
		t.Fatalf("sealed unknown history %v", e)
	}
}

// Independent review r3 proved that an unknown extra hardlink could outlive GC
// while its full object reservation was released. Validate all known names
// before the first unlink, for both staging and publication crash states.
func TestReworkRefsGCUnknownHardlinkRetainsEveryKnownPathAndQuota(t *testing.T) {
	for _, phase := range []string{"staging", "published-pair"} {
		t.Run(phase, func(t *testing.T) {
			f, data := refsFixture(t)
			ctx := context.Background()
			u := beginRefs(t, f, data)
			uploadRefs(t, f, data, u.UploadID)
			if phase == "published-pair" {
				f.db.reworkFault = func(cut string) error {
					if cut == "published_link" {
						return errors.New("crash")
					}
					return nil
				}
				if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "commit", UploadID: u.UploadID}); e == nil {
					t.Fatal("missing crash")
				}
				f.db.reworkFault = nil
			}
			var id string
			f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
			stage := filepath.Join(f.db.objectRoot(), id+".stage")
			final := filepath.Join(f.db.objectRoot(), id+".json")
			extra := filepath.Join(f.db.objectRoot(), "unregistered-extra-link")
			if e := os.Link(stage, extra); e != nil {
				t.Fatal(e)
			}
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
				t.Fatal(e)
			}
			var before int64
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
				t.Fatal("unknown link released reservation")
			}
			var after int64
			var status string
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
			f.db.sql.QueryRow(`SELECT status FROM rework_objects WHERE id=?`, id).Scan(&status)
			if before != after || status != "deleting" {
				t.Fatalf("unknown link released quota %d/%d status=%s", before, after, status)
			}
			for _, path := range []string{stage, extra} {
				if _, e := os.Lstat(path); e != nil {
					t.Fatalf("deleted before complete preflight %s: %v", path, e)
				}
			}
			if phase == "published-pair" {
				if _, e := os.Lstat(final); e != nil {
					t.Fatalf("known final deleted before preflight %v", e)
				}
			}
			// Explicit resolution of the unknown alias restores safe known-link GC.
			if e := os.Remove(extra); e != nil {
				t.Fatal(e)
			}
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e != nil {
				t.Fatal(e)
			}
			for _, path := range []string{stage, final} {
				if _, e := os.Lstat(path); !os.IsNotExist(e) {
					t.Fatalf("known link retained %s %v", path, e)
				}
			}
		})
	}
}

func TestReworkRefsGCLateHardlinkKeepsChargeAndMissingIdentityFailsClosed(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	var id string
	f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
	stage := filepath.Join(f.db.objectRoot(), id+".stage")
	extra := filepath.Join(f.db.objectRoot(), "late-extra-link")
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	var before int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	linked := false
	f.db.reworkFault = func(cut string) error {
		if cut == "gc_before_unlink" && !linked {
			linked = true
			return os.Link(stage, extra)
		}
		return nil
	}
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
		t.Fatal("late alias released reservation")
	}
	f.db.reworkFault = nil
	if !linked {
		t.Fatal("late-link fault was not reached")
	}
	for i := 0; i < 2; i++ {
		var after int64
		f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
		if after != before {
			t.Fatalf("late alias released %d", before-after)
		}
		if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
			t.Fatal("missing known identity guessed deletion complete")
		}
	}
	// Explicit owner resolution restores a known alias of the original inode.
	if e := os.Link(extra, stage); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(extra); e != nil {
		t.Fatal(e)
	}
	resolveOwnedTestQuarantine(t, f, u.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
}

func TestReworkRefsGCLatePathReplacementNeverDeletesUnknownEntry(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f, data := refsFixture(t)
			ctx := context.Background()
			u := beginRefs(t, f, data)
			uploadRefs(t, f, data, u.UploadID)
			var id string
			f.db.sql.QueryRow(`SELECT object_id FROM rework_uploads WHERE id=?`, u.UploadID).Scan(&id)
			stage := filepath.Join(f.db.objectRoot(), id+".stage")
			moved := filepath.Join(f.db.objectRoot(), "registered-moved")
			var foreign os.FileInfo
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
				t.Fatal(e)
			}
			var before int64
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
			replaced := false
			f.db.reworkFault = func(cut string) error {
				if cut != "gc_before_unlink" || replaced {
					return nil
				}
				replaced = true
				if e := os.Rename(stage, moved); e != nil {
					return e
				}
				var e error
				switch kind {
				case "file":
					e = os.WriteFile(stage, []byte("unregistered foreign inode"), 0600)
				case "symlink":
					e = os.Symlink(moved, stage)
				case "directory":
					e = os.Mkdir(stage, 0700)
					if e == nil {
						e = os.WriteFile(filepath.Join(stage, "foreign-child"), []byte("preserve"), 0600)
					}
				}
				if e != nil {
					return e
				}
				foreign, e = os.Lstat(stage)
				return e
			}
			if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil || !replaced {
				t.Fatalf("replacement fault not exercised: %v", e)
			}
			f.db.reworkFault = nil
			found := false
			walkErr := filepath.WalkDir(f.db.objectRoot(), func(path string, _ os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				info, e := os.Lstat(path)
				if e != nil {
					return e
				}
				if os.SameFile(foreign, info) {
					found = true
					if kind == "directory" {
						if _, e = os.Stat(filepath.Join(path, "foreign-child")); e != nil {
							return e
						}
					}
				}
				return nil
			})
			if walkErr != nil {
				t.Fatal(walkErr)
			}
			if !found {
				t.Fatal("GC deleted the unregistered replacement entry")
			}
			if _, e := os.Stat(moved); e != nil {
				t.Fatalf("registered original lost: %v", e)
			}
			var after int64
			var status string
			f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
			f.db.sql.QueryRow(`SELECT status FROM rework_objects WHERE id=?`, id).Scan(&status)
			if before != after || status != "deleting" {
				t.Fatalf("unknown replacement released quota %d/%d %s", before, after, status)
			}
		})
	}
}

// Explicit operator recovery in this isolated test: remove only captured aliases
// whose inode still matches registry, then the now-empty test-owned barrier.
func resolveOwnedTestQuarantine(t *testing.T, f ownerReworkFixture, upload string) {
	t.Helper()
	entries, e := os.ReadDir(f.db.objectRoot())
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".gc-"+upload+"-") {
			continue
		}
		path := filepath.Join(f.db.objectRoot(), entry.Name())
		files, e := os.ReadDir(path)
		if e != nil {
			t.Fatal(e)
		}
		for _, file := range files {
			id := strings.TrimSuffix(strings.TrimSuffix(file.Name(), ".stage"), ".json")
			var dev, ino uint64
			if e = f.db.sql.QueryRow(`SELECT device,inode FROM rework_objects WHERE id=?`, id).Scan(&dev, &ino); e != nil {
				t.Fatal(e)
			}
			info, e := os.Lstat(filepath.Join(path, file.Name()))
			if e != nil {
				t.Fatal(e)
			}
			st := info.Sys().(*syscall.Stat_t)
			if !info.Mode().IsRegular() || uint64(st.Dev) != dev || st.Ino != ino {
				t.Fatal("unknown quarantined entry cannot be removed")
			}
			if e = os.Remove(filepath.Join(path, file.Name())); e != nil {
				t.Fatal(e)
			}
		}
		if e = os.Remove(path); e != nil {
			t.Fatal(e)
		}
	}
}

func TestReworkRefsQuarantineCrashStaysChargedAndDoesNotAccumulate(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	var before int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	f.db.reworkFault = func(cut string) error {
		if cut == "gc_after_capture" {
			return errors.New("crash after durable capture")
		}
		return nil
	}
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil {
		t.Fatal("missing crash cut")
	}
	f.db.reworkFault = nil
	path := f.db.path
	f.db.Close()
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	f.db = db
	for i := 0; i < 3; i++ {
		if _, e = db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID}); e == nil || e.Error() != string(quarantinePending) {
			t.Fatalf("ambiguous capture guessed success %v", e)
		}
	}
	entries, e := os.ReadDir(db.objectRoot())
	if e != nil {
		t.Fatal(e)
	}
	dirs := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gc-"+u.UploadID+"-") {
			dirs++
		}
	}
	var after int64
	db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
	if dirs != 1 || before != after {
		t.Fatalf("unbounded or uncharged quarantine dirs=%d bytes=%d/%d", dirs, before, after)
	}
}

func TestReworkRefsSuccessfulGCRetainsOneBoundedBarrierAndMetadata(t *testing.T) {
	f, data := refsFixture(t)
	ctx := context.Background()
	u := beginRefs(t, f, data)
	uploadRefs(t, f, data, u.UploadID)
	if _, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "abort", UploadID: u.UploadID}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		r, e := f.db.ReworkRefs(ctx, "owner", f.spec, ReworkRefsRequest{Phase: "gc", UploadID: u.UploadID})
		if e != nil || r.Status != "removed" || r.CleanupReason != "upload_tombstone_and_quarantine_barrier_retained" {
			t.Fatalf("GC claimed complete runtime cleanup %+v %v", r, e)
		}
	}
	entries, e := os.ReadDir(f.db.objectRoot())
	if e != nil {
		t.Fatal(e)
	}
	dirs := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gc-"+u.UploadID+"-") {
			dirs++
			inside, e := os.ReadDir(filepath.Join(f.db.objectRoot(), entry.Name()))
			if e != nil || len(inside) != 0 {
				t.Fatalf("normal quarantine contains unexpected entries %v %v", inside, e)
			}
		}
	}
	if dirs != 1 {
		t.Fatalf("unbounded normal barriers %d", dirs)
	}
}
