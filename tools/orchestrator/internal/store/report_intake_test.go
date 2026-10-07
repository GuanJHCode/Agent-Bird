package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestReportArtifactIsManagedBeforeDurableACK(t *testing.T) {
	db, launch := quotaFixture(t)
	if err := os.Chmod(filepath.Dir(db.path), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	const contents = "provider-owned result must survive its source"
	source := filepath.Join(root, "result.bin")
	if err = os.WriteFile(source, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	registration := contract.ReportCapabilityRegistration{CapabilityID: launch.ReportCapabilityID, ProducerID: launch.HostID, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: launch.WorkRevision, ExecutionEpoch: launch.ExecutionEpoch, TokenHash: runtimeHash("token"), CapabilityDir: root}
	if _, err = db.RegisterReportCapability(ctx, registration); err != nil {
		t.Fatal(err)
	}
	ref := contract.ArtifactRef{ID: "result", Path: source, SHA256: runtimeHash(contents), Size: int64(len(contents))}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "artifact": ref})
	spec := ReportEventSpec{CapabilityID: launch.ReportCapabilityID, Token: "token", EventID: "result", Sequence: 1, Kind: "result", Payload: raw}
	ack, err := db.CommitReportEvent(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	if err = db.sql.QueryRow(`SELECT body_json FROM report_events WHERE event_id='result'`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var event contract.Event
	if err = json.Unmarshal([]byte(saved), &event); err != nil || event.Artifact == nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(filepath.Dir(db.path), "managed-artifacts") + string(filepath.Separator)
	if event.Artifact.Path == source || !strings.HasPrefix(event.Artifact.Path, managedRoot) {
		t.Fatal("durable ACK still depends on provider-owned source", event.Artifact.Path)
	}
	before, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, []byte(contents)) {
		t.Fatal("intake modified the provider source", err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(event.Artifact.Path)
	if err != nil || !bytes.Equal(retained, []byte(contents)) || event.Artifact.SHA256 != ref.SHA256 || event.Artifact.Size != ref.Size {
		t.Fatal("committed artifact did not survive source removal", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.CommitReportEvent(ctx, spec)
	if err != nil || again != ack {
		t.Fatal("exact report replay reread source or changed ACK", again, err)
	}
}

func newReportIntakeFixture(t *testing.T) (*DB, ReportEventSpec, string) {
	t.Helper()
	db, g := quotaFixture(t)
	if err := os.Chmod(filepath.Dir(db.path), 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "result.bin")
	const body = "original report bytes"
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := db.RegisterReportCapability(context.Background(), contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: g.HostID, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, TokenHash: runtimeHash("token"), CapabilityDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "artifact": contract.ArtifactRef{ID: "result", Path: source, Size: int64(len(body)), SHA256: runtimeHash(body)}})
	return db, ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: "result", Sequence: 1, Kind: "result", Payload: raw}, source
}

func TestReportIntakeRevalidatesBeforeEventCommit(t *testing.T) {
	for _, kind := range []string{"device", "inode", "missing-data", "replaced-data", "replaced-parent", "missing-intent", "changed-intent"} {
		t.Run(kind, func(t *testing.T) {
			db, spec, _ := newReportIntakeFixture(t)
			_, err := db.commitReportEventWithCopy(context.Background(), spec, func(ctx context.Context, root string, desc artifacts.Intake) (artifacts.Proof, error) {
				proof, err := artifacts.Materialize(ctx, root, desc)
				if err != nil {
					return proof, err
				}
				dir := filepath.Dir(proof.Path)
				switch kind {
				case "device":
					proof.Device++
				case "inode":
					proof.Inode++
				case "missing-data":
					err = os.Remove(proof.Path)
				case "replaced-data":
					var body []byte
					body, err = os.ReadFile(proof.Path)
					if err == nil {
						err = os.Rename(proof.Path, proof.Path+".original")
					}
					if err == nil {
						err = os.WriteFile(proof.Path, body, 0600)
					}
				case "replaced-parent":
					err = os.Rename(dir, dir+"-original")
					if err == nil {
						err = os.Mkdir(dir, 0700)
					}
					if err == nil {
						for _, name := range []string{"data.bin", "intent.json", "writer.lock"} {
							if err = os.Rename(filepath.Join(dir+"-original", name), filepath.Join(dir, name)); err != nil {
								break
							}
						}
					}
				case "missing-intent":
					err = os.Remove(filepath.Join(dir, "intent.json"))
				case "changed-intent":
					err = os.WriteFile(filepath.Join(dir, "intent.json"), []byte("{}"), 0600)
				}
				return proof, err
			})
			if err == nil {
				t.Fatal("stale or forged materialization proof committed an event")
			}
			var events, refs, active int
			var phase string
			if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM report_events),(SELECT COUNT(*) FROM managed_artifact_refs),(SELECT COUNT(*) FROM managed_artifact_reservations WHERE state='active'),phase FROM managed_artifact_intakes`).Scan(&events, &refs, &active, &phase); err != nil || events != 0 || refs != 0 || active != 0 || phase != "prepared" {
				t.Fatal("failed verification changed durable event state", events, refs, active, phase, err)
			}
		})
	}
}
