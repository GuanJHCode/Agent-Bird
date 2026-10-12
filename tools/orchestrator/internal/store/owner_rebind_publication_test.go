package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
)

func publicationFixture(t *testing.T) (originalOwnerFixture, RebindOwnerSpec, OwnerRebindPublication) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	f := newOriginalOwnerFixtureWithPlanAtPath(t, nil, nil, filepath.Join(root, "state.db"))
	spec := authorizationRebindSpec(f, 1)
	h := f.prepare.Host
	r, err := f.db.ResolveOwnerRebindPublication(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	metadata := hostbootstrap.MetadataV2{Version: 2, ProducerID: h.LaunchID, SocketPath: filepath.Join(r.Authority.Nodes().StateRoot, "coordinator.sock"), SpoolRoot: filepath.Join(r.Authority.Nodes().StateRoot, "host-spool", h.LaunchID), Hello: hostbootstrap.HelloMetadata{LaunchID: h.LaunchID, OriginContextID: spec.OriginContextID, OriginPID: spec.OriginPID, OriginBirth: spec.OriginBirth, HostGeneration: spec.HostGeneration, Executable: h.Executable}}
	raw, _ := json.Marshal(metadata)
	raw = append(raw, '\n')
	sum := sha256.Sum256(raw)
	return f, spec, OwnerRebindPublication{RunID: spec.RunID, HostGeneration: spec.HostGeneration, LaunchID: h.LaunchID, PriorSHA256: strings.Repeat("b", 64), ExpectedSHA256: hex.EncodeToString(sum[:]), BootstrapJSON: raw, Facts: OwnerRebindPublicationFacts{Root: r.Authority.Nodes(), OriginContextID: r.Origin.OriginContextID, OldPID: r.Origin.OriginPID, OldBirth: r.Origin.OriginBirth, OldGeneration: r.Generation, OriginPID: spec.OriginPID, OriginBirth: spec.OriginBirth, AttachmentProofSHA256: spec.AttachmentProofSHA256}}

}

func TestOwnerPublicationFinalizeRejectsDrift(t *testing.T) {
	for _, kind := range []string{"owner", "root"} {
		t.Run(kind, func(t *testing.T) {
			f, spec, p := publicationFixture(t)
			ctx := context.Background()
			p, err := f.db.PrepareOwnerRebindPublication(ctx, spec, p)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner" {
				if _, err = f.db.SQL().Exec(`UPDATE runs SET origin_birth='replacement' WHERE id=?`, spec.RunID); err != nil {
					t.Fatal(err)
				}
			} else {
				root := filepath.Dir(f.db.configIdentity.StateRoot + "/state.db")
				moved := root + "-saved"
				if err = os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.Rename(moved, root) })
				if err = os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err = f.db.FinalizeOwnerRebindPublication(ctx, p); err == nil {
				t.Fatal("finalized publication after authority drift")
			}
			var state string
			if err = f.db.SQL().QueryRow(`SELECT state FROM owner_rebind_publications`).Scan(&state); err != nil || state != "pending" {
				t.Fatal("lost pending intent", state, err)
			}
		})
	}
}

func publicationUsage(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='owner-run'),0)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestOwnerPublicationJournalQuotaAndReplay(t *testing.T) {
	ctx := context.Background()
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("quota_short_%v", short), func(t *testing.T) {
			f, spec, p := publicationFixture(t)
			fixed := "2026-10-09T00:00:00Z"
			p.CreatedAt = fixed
			row := ownerRebindRow{RunID: p.RunID, Generation: p.HostGeneration, OldGeneration: p.Facts.OldGeneration, PID: spec.OriginPID, Birth: spec.OriginBirth, Proof: spec.AttachmentProofSHA256, Created: fixed}
			before := publicationUsage(t, f.db.sql)
			cost := row.bytes() + p.bytes() + int64(len(p.BootstrapJSON))
			f.db.runControlLimit = before + cost
			if short {
				f.db.runControlLimit--
			}
			err := f.db.rebindOwnerWithPublication(ctx, spec, func() string { return fixed }, &p)
			if short {
				if err == nil || publicationUsage(t, f.db.sql) != before {
					t.Fatal("short admission changed state", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pending := publicationUsage(t, f.db.sql)
			if pending != before+cost {
				t.Fatal("undercharged immutable journal or scratch", pending-before, cost)
			}
			replay, err := f.db.PrepareOwnerRebindPublication(ctx, spec, p)
			if err != nil || publicationUsage(t, f.db.sql) != pending {
				t.Fatal("pending replay charged twice", err)
			}
			bad := p
			bad.PriorSHA256 = strings.Repeat("c", 64)
			if _, err = f.db.PrepareOwnerRebindPublication(ctx, spec, bad); err == nil {
				t.Fatal("changed prior accepted")
			}
			next := authorizationRebindSpec(f, 2)
			if err = f.db.RebindOwner(ctx, next); err == nil {
				t.Fatal("legacy rebind bypassed pending gate")
			}
			if err = f.db.FinalizeOwnerRebindPublication(ctx, replay); err != nil {
				t.Fatal(err)
			}
			released := publicationUsage(t, f.db.sql)
			if pending-released != int64(len(p.BootstrapJSON)) {
				t.Fatal("scratch release mismatch", pending-released)
			}
			if err = f.db.FinalizeOwnerRebindPublication(ctx, replay); err != nil || publicationUsage(t, f.db.sql) != released {
				t.Fatal("published exact replay charged/refunded twice", err)
			}
			for _, q := range []string{`UPDATE owner_rebind_publications SET prior_sha256='x'`, `DELETE FROM owner_rebind_publications`, `UPDATE owner_rebind_publications SET published_at='changed'`} {
				if _, err = f.db.sql.Exec(q); err == nil {
					t.Fatal("mutable immutable journal", q)
				}
			}
		})
	}
}
func TestOwnerPublicationFailedCommitRetainsPending(t *testing.T) {
	for _, stage := range []string{"prepare", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			f, spec, p := publicationFixture(t)
			ctx := context.Background()
			var err error
			if stage == "finalize" {
				p, err = f.db.PrepareOwnerRebindPublication(ctx, spec, p)
				if err != nil {
					t.Fatal(err)
				}
			}
			trigger := `AFTER INSERT ON owner_rebind_publications`
			if stage == "finalize" {
				trigger = `AFTER UPDATE ON owner_rebind_publications`
			}
			if _, err = f.db.sql.Exec(`CREATE TABLE publication_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER fail_publication_commit ` + trigger + ` BEGIN INSERT INTO publication_commit_fault VALUES('missing-parent');END;`); err != nil {
				t.Fatal(err)
			}
			before := publicationUsage(t, f.db.sql)
			if stage == "prepare" {
				_, err = f.db.PrepareOwnerRebindPublication(ctx, spec, p)
			} else {
				err = f.db.FinalizeOwnerRebindPublication(ctx, p)
			}
			if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatal("expected physical COMMIT failure", err)
			}
			external, err := sql.Open("sqlite", f.db.path)
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			for _, db := range []*sql.DB{f.db.sql, external} {
				var count, faults int
				var state string
				if err = db.QueryRow(`SELECT count(*),COALESCE(MIN(state),''),(SELECT count(*) FROM publication_commit_fault) FROM owner_rebind_publications`).Scan(&count, &state, &faults); err != nil {
					t.Fatal(err)
				}
				if faults != 0 || publicationUsage(t, db) != before || (stage == "prepare" && count != 0) || (stage == "finalize" && (count != 1 || state != "pending")) {
					t.Fatal("COMMIT leaked publication/accounting", count, state, faults)
				}
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER fail_publication_commit`); err != nil {
				t.Fatal(err)
			}
			p, err = f.db.PrepareOwnerRebindPublication(ctx, spec, p)
			if err != nil {
				t.Fatal("exact repair after rollback", err)
			}
			if err = f.db.FinalizeOwnerRebindPublication(ctx, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
