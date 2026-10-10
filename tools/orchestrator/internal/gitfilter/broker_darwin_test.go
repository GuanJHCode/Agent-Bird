//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
)

func brokerRequest(t *testing.T, f *fixture, mode string) BrokerRequest {
	t.Helper()
	base, err := os.MkdirTemp(f.root, "broker-")
	if err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(base, "a", "s")
	sp, err := events.Open(segment)
	if err != nil {
		t.Fatal(err)
	}
	m := contract.GitFilterManifestV1{Version: 1, DriverID: "fixture", Paths: []string{"file.txt"}, Git: contract.GitFilterGitPin{Path: f.tool.Path, Version: "fixture", SHA256: f.tool.SHA256}, Clean: contract.GitFilterToolPin{Path: f.tool.Path, SHA256: f.tool.SHA256, Args: []string{mode}}, Smudge: contract.GitFilterToolPin{Path: f.tool.Path, SHA256: f.tool.SHA256, Args: []string{mode}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: strings.Repeat("b", 64)}
	sha, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "a", SegmentID: "s", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1}
	scope, err := contract.HostArtifactScopeSHA256(c)
	if err != nil {
		t.Fatal(err)
	}
	c.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: "terminal", ProgressReservationID: "progress", ScopeSHA256: scope}
	filterScope, err := contract.GitFilterResourceScopeSHA256(c, sha)
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.GitFilterResourceGrant{Version: 1, ReservationID: "filter", ScopeSHA256: filterScope, ManifestSHA256: sha, CleanToolSHA256: f.tool.SHA256, SmudgeToolSHA256: f.tool.SHA256, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}
	return BrokerRequest{Launch: c, Grant: grant, Manifest: m, SegmentDirectory: segment, Spool: sp}
}
func liveBroker(t *testing.T, ctx context.Context, f *fixture, r BrokerRequest) *Broker {
	t.Helper()
	b, err := NewBroker(ctx, r)
	if b != nil {
		t.Cleanup(func() { b.Close() })
	}
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	return b
}
func brokerTransform(t *testing.T, ctx context.Context, f *fixture, b *Broker, op string, src, dst int) (Result, error) {
	t.Helper()
	r, e := b.Transform(ctx, op, src, dst)
	if r.Unknown || r.Started && (!r.WaitProved || !r.TreeProved) {
		f.retain = true
	}
	t.Logf("broker operation=%s started=%t wait=%t tree=%t exit=%d unknown=%t accepted=%t error=%v", op, r.Started, r.WaitProved, r.TreeProved, r.ExitCode, r.Unknown, r.Accepted, e)
	return r, e
}
func TestFilterBrokerOwnedResources(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Run("round_trip_and_live_cleanup", func(t *testing.T) {
		req := brokerRequest(t, f, "xor")
		b := liveBroker(t, ctx, f, req)
		input := []byte("canonical\x00content\n")
		source := f.request(t, "copy", input).Input
		if err := b.Load(ctx, 0, source); err != nil {
			t.Fatal(err)
		}
		one, err := brokerTransform(t, ctx, f, b, "smudge", 0, 1)
		if err != nil || !one.Accepted {
			t.Fatal(err)
		}
		var materialized bytes.Buffer
		if err = b.ReadSlot(ctx, 1, &materialized); err != nil || bytes.Equal(materialized.Bytes(), input) {
			t.Fatalf("not smudged: %v", err)
		}
		two, err := brokerTransform(t, ctx, f, b, "clean", 1, 2)
		if err != nil || !two.Accepted {
			t.Fatal(err)
		}
		var canonical bytes.Buffer
		if err = b.ReadSlot(ctx, 2, &canonical); err != nil || !bytes.Equal(canonical.Bytes(), input) {
			t.Fatalf("roundtrip: %v", err)
		}
		state := b.State()
		if !state.EverStarted || state.Unknown || state.StartedAt != one.StartedAt || state.TerminalAt != two.TerminalAt {
			t.Fatal("lost live span")
		}
		if cold, e := NewBroker(ctx, req); e == nil || cold != nil {
			t.Fatal("cold object adopted existing resources")
		}
		heldFiles := []*os.File{b.nodes["clean/tool"].file, b.nodes["smudge/tool"].file, b.slots[0], b.slots[1], b.slots[2]}
		receipt, err := b.Cleanup(ctx)
		if err != nil || receipt == nil || receipt.Validate() != nil || receipt.ReservationID != req.Grant.ReservationID || receipt.RemovedBytes != 2*f.tool.Size+3*int64(len(input)) {
			t.Fatalf("cleanup: %+v %v", receipt, err)
		}
		for _, file := range heldFiles {
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("cleanup receipt refunded bytes while original file descriptor remained open")
			}
		}
		if _, err = os.Lstat(b.resource); !os.IsNotExist(err) {
			t.Fatalf("resource directory remains: %v", err)
		}
		var journal struct {
			Phase string `json:"phase"`
		}
		raw, err := os.ReadFile(filepath.Join(req.SegmentDirectory, "git-filter.json"))
		if err != nil || json.Unmarshal(raw, &journal) != nil || journal.Phase != "removed" {
			t.Fatal("removed not durable")
		}
		if _, err = b.Transform(ctx, "clean", 0, 1); err == nil {
			t.Fatal("used removed broker")
		}
	})
	t.Run("slots_are_initialized_and_nonaliasing", func(t *testing.T) {
		req := brokerRequest(t, f, "copy")
		b := liveBroker(t, ctx, f, req)
		var out bytes.Buffer
		if b.ReadSlot(ctx, 0, &out) == nil {
			t.Fatal("uninitialized slot read")
		}
		if r, e := b.Transform(ctx, "clean", 0, 1); e == nil || r.Started {
			t.Fatal("uninitialized slot executed")
		}
		source := f.request(t, "copy", []byte("keep-me")).Input
		if err := b.Load(ctx, 0, source); err != nil {
			t.Fatal(err)
		}
		alias, err := os.Open(b.slots[0].Name())
		if err != nil {
			t.Fatal(err)
		}
		defer alias.Close()
		if b.Load(ctx, 0, alias) == nil {
			t.Fatal("same inode input accepted")
		}
		if err = b.ReadSlot(ctx, 0, &out); err != nil || out.String() != "keep-me" {
			t.Fatal("alias attempt truncated slot")
		}
		if r, e := b.Transform(ctx, "clean", 0, 0); e == nil || r.Started {
			t.Fatal("same output slot executed")
		}
		if r, e := b.Cleanup(ctx); e != nil || r == nil || b.State().EverStarted {
			t.Fatalf("never-started cleanup %v", e)
		}
	})
	t.Run("known_failure_is_terminal", func(t *testing.T) {
		req := brokerRequest(t, f, "fail")
		b := liveBroker(t, ctx, f, req)
		if err := b.Load(ctx, 0, f.request(t, "copy", nil).Input); err != nil {
			t.Fatal(err)
		}
		r, e := brokerTransform(t, ctx, f, b, "clean", 0, 1)
		if e == nil || r.ExitCode != 7 || !r.WaitProved || !r.TreeProved || r.Unknown {
			t.Fatal("missing original failure proof")
		}
		var out bytes.Buffer
		if b.ReadSlot(ctx, 1, &out) == nil {
			t.Fatal("failed output consumed")
		}
		if r, e := b.Transform(ctx, "clean", 0, 2); e == nil || r.Started {
			t.Fatal("failed broker retried")
		}
		if cleanup, e := b.Cleanup(ctx); e != nil || cleanup == nil {
			t.Fatalf("known terminal cleanup %v", e)
		}
	})
	t.Run("journal_stage_conflict_preserves_resources", func(t *testing.T) {
		req := brokerRequest(t, f, "copy")
		b := liveBroker(t, ctx, f, req)
		if err := b.Load(ctx, 0, f.request(t, "copy", []byte("a")).Input); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(req.SegmentDirectory, ".git-filter.json.tmp")
		sentinel := []byte("foreign-stage")
		if err := os.WriteFile(stage, sentinel, 0600); err != nil {
			t.Fatal(err)
		}
		r, e := b.Transform(ctx, "clean", 0, 1)
		if e == nil || r.Started || !b.State().Unknown {
			t.Fatal("stage conflict not conservative")
		}
		if cleanup, e := b.Cleanup(ctx); e == nil || cleanup != nil {
			t.Fatal("unknown refunded")
		}
		if got, e := os.ReadFile(stage); e != nil || !bytes.Equal(got, sentinel) {
			t.Fatal("foreign stage removed")
		}
		if _, e := os.Stat(b.resource); e != nil {
			t.Fatal("unknown resources deleted")
		}
		// Injected stage prevents Execute entirely. Fixture teardown owns these
		// test files and has no unproved process; production Cleanup refused.
	})
	t.Run("wrong_spool_and_tool_drift_are_refused", func(t *testing.T) {
		req := brokerRequest(t, f, "copy")
		other, err := events.Open(filepath.Join(req.SegmentDirectory, "other"))
		if err != nil {
			t.Fatal(err)
		}
		wrong := req
		wrong.Spool = other
		if b, e := NewBroker(ctx, wrong); e == nil || b != nil {
			t.Fatal("wrong spool accepted")
		}
		b := liveBroker(t, ctx, f, req)
		if err := os.Chmod(filepath.Join(b.resource, "clean", "tool"), 0700); err != nil {
			t.Fatal(err)
		}
		if cleanup, e := b.Cleanup(ctx); e == nil || cleanup != nil {
			t.Fatal("drifted inventory deleted")
		}
		if _, e := os.Stat(filepath.Join(b.resource, "clean", "tool")); e != nil {
			t.Fatal("drifted tool absent")
		}
	})
}
