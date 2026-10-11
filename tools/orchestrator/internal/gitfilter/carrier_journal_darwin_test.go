//go:build darwin

package gitfilter

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func carrierJournalFixture(t *testing.T) (contract.LaunchCommand, *events.Spool) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sp, err := events.Open(filepath.Join(root, "attempt", "segment"))
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	m := contract.GitFilterManifestV1{Version: 1, DriverID: "fixture", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: "/usr/bin/git", Version: "fixture", SHA256: h}, Clean: contract.GitFilterToolPin{Path: "/private/clean", SHA256: h, Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: "/private/smudge", SHA256: h, Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: h}
	c := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1}
	c.AdapterPayload, _ = json.Marshal(map[string]any{"kind": "candidate", "candidate_action": map[string]any{"version": 1, "operation": "filter_integrate"}, "git_filter": m})
	hs, _ := contract.HostArtifactScopeSHA256(c)
	c.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: "terminal", ProgressReservationID: "progress", ScopeSHA256: hs}
	md, _ := m.Digest()
	scope, _ := contract.GitFilterResourceScopeSHA256(c, md)
	c.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: scope, ManifestSHA256: md, CleanToolSHA256: h, SmudgeToolSHA256: h, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}
	cs, _ := contract.GitFilterCarrierScopeSHA256(c)
	c.GitFilterCarrier = &contract.GitFilterCarrierGrant{Version: 1, ReservationID: "carrier", ScopeSHA256: cs, FilterReservationID: "filter", FilterScopeSHA256: scope, ManifestSHA256: md, MetadataBytes: contract.GitFilterCarrierMetadataBytes, RuntimeNamespace: contract.GitFilterCarrierNamespace, DirectorySlots: 1, SocketSlots: 1}
	return c, sp
}

type carrierCollisionJournal struct {
	original *carrierJournal
	foreign  string
}

func (j *carrierCollisionJournal) RecordCarrierSocket(ctx context.Context, r carrierSocketRecord) error {
	if err := j.original.RecordCarrierSocket(ctx, r); err != nil {
		return err
	}
	if r.Phase == "reserved" {
		j.foreign = r.Directory
		return os.Mkdir(r.Directory, 0700)
	}
	return nil
}

func TestCarrierCompanionKnownNotCreated(t *testing.T) {
	c, sp := carrierJournalFixture(t)
	j, err := newCarrierJournal(c, sp)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	ctx := carrierTestContext(t)
	collision := &carrierCollisionJournal{original: j}
	o, err := newCarrierSocket(ctx, sp.Directory(), j.binding, collision)
	defer func() { carrierTestRelease(t, o, collision.foreign) }()
	if err == nil || o == nil {
		t.Fatal("collision not observed")
	}
	if err = o.release(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := j.cleanup(o)
	if err != nil || r.Outcome != "not_created" || r.FinalOrdinal != 2 || r.RetainedBytes == 0 {
		t.Fatal("known collision did not settle", r, err)
	}
	if _, err = os.Stat(collision.foreign); err != nil {
		t.Fatal("foreign entry changed", err)
	}
}
func TestCarrierCompanionJournalNative(t *testing.T) {
	for _, mode := range []string{"lifecycle", "foreign-current", "foreign-temporary", "replaced-current"} {
		t.Run(mode, func(t *testing.T) {
			c, sp := carrierJournalFixture(t)
			if strings.HasPrefix(mode, "foreign-") {
				name := carrierJournalName
				if mode == "foreign-temporary" {
					name = "." + name + ".tmp"
				}
				file := filepath.Join(sp.Directory(), name)
				if err := os.WriteFile(file, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
				if j, err := newCarrierJournal(c, sp); err == nil || j != nil {
					t.Fatal("adopted foreign journal")
				}
				body, _ := os.ReadFile(file)
				if string(body) != "foreign" {
					t.Fatal("foreign bytes changed")
				}
				return
			}
			j, err := newCarrierJournal(c, sp)
			if err != nil {
				t.Fatal(err)
			}
			defer j.close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			o, err := newCarrierSocket(ctx, sp.Directory(), j.binding, j)
			defer carrierTestRelease(t, o)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "replaced-current" {
				path := filepath.Join(sp.Directory(), carrierJournalName)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path+".replacement", body, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path+".replacement", path); err != nil {
					t.Fatal(err)
				}
				if err = o.close(ctx); err != nil {
					t.Fatal(err)
				}
				if o.remove(ctx) == nil {
					t.Fatal("replacement accepted")
				}
				if _, err = j.cleanup(o); err == nil {
					t.Fatal("uncertain carrier released")
				}
				return
			}
			b := j.binding.Original
			for i := uint64(1); i <= 8; i++ {
				r := ExactTransformRecord{Phase: "intent", Sequence: i, ManifestSHA256: b.ManifestSHA256, ScopeSHA256: b.ScopeSHA256, ReservationID: b.ReservationID, AttemptID: b.AttemptID, PlanRevision: b.PlanRevision, PermitsSHA256: strings.Repeat("b", 64), Path: "file", Operation: "smudge"}
				if err = j.RecordExactTransform(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			if err = o.close(ctx); err != nil {
				t.Fatal(err)
			}
			if err = o.remove(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := j.cleanup(o)
			if err != nil || r.Validate() != nil || r.FinalOrdinal != 12 || r.RetainedBytes > contract.GitFilterJournalLimit+1 {
				t.Fatal("invalid bounded receipt", r, err)
			}
			body, err := os.ReadFile(filepath.Join(sp.Directory(), carrierJournalName))
			if err != nil || int64(len(body)) != r.RetainedBytes {
				t.Fatal("journal size", err)
			}
			var snapshot carrierJournalSnapshot
			if json.Unmarshal(body, &snapshot) != nil || snapshot.Transform.Sequence != 8 || snapshot.Ordinal != 12 || snapshot.Socket.Phase != "removed" {
				t.Fatal("unbounded or wrong final journal")
			}
			if _, err = os.Lstat(filepath.Join(sp.Directory(), "."+carrierJournalName+".tmp")); !os.IsNotExist(err) {
				t.Fatal("temporary journal retained", err)
			}
		})
	}
}
