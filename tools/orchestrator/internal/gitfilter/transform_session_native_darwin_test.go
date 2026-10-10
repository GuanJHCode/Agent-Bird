//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"
)

func TestExactTransformSessionNativeBroker(t *testing.T) {
	var f *fixture
	t.Cleanup(func() {
		if f == nil || f.retain {
			return
		}
		if _, err := os.Lstat(f.root); !os.IsNotExist(err) {
			t.Errorf("owned root not absent: %v", err)
		} else {
			t.Logf("owned_fixture_absent=%s", f.root)
		}
	})
	f = newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Run("actual_b_r_roundtrip", func(t *testing.T) {
		req := brokerRequest(t, f, "xor")
		b := liveBroker(t, ctx, f, req)
		canonical := []byte("canonical\x00content\n")
		rendered := append([]byte(nil), canonical...)
		for i := range rendered {
			rendered[i] ^= 0x5a
		}
		permits := []ExactTransformPermit{
			{Path: "file.txt", Operation: "smudge", InputSHA256: exactHash(canonical), InputBytes: int64(len(canonical)), OutputSHA256: exactHash(rendered), OutputBytes: int64(len(rendered)), MaximumCalls: 1},
			{Path: "file.txt", Operation: "clean", InputSHA256: exactHash(rendered), InputBytes: int64(len(rendered)), OutputSHA256: exactHash(canonical), OutputBytes: int64(len(canonical)), MaximumCalls: 1},
		}
		j := &exactTestJournal{}
		s, err := OpenExactTransformSession(b, b.Binding(), req.Manifest, permits, j)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if s.State().Unknown || b.State().Unknown {
				f.retain = true
			}
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
		out, err := s.Exchange(ctx, 1, "file.txt", "smudge", exactProduce(string(canonical)))
		if err != nil || !bytes.Equal(out, rendered) {
			t.Fatalf("actual rendered output: %x %v", out, err)
		}
		back, err := s.Exchange(ctx, 2, "file.txt", "clean", exactProduce(string(out)))
		if err != nil || !bytes.Equal(back, canonical) {
			t.Fatalf("actual canonical output: %x %v", back, err)
		}
		if len(j.records) != 4 {
			t.Fatalf("journal count=%d", len(j.records))
		}
		for i, r := range j.records {
			if r.Sequence != uint64(i/2+1) || r.ScopeSHA256 != req.Grant.ScopeSHA256 || r.ReservationID != req.Grant.ReservationID {
				t.Fatalf("wrong original journal binding: %+v", r)
			}
			if i%2 == 1 {
				if r.Phase != "terminal" || !r.Started || !r.WaitProved || !r.TreeProved || r.Unknown {
					t.Fatalf("original terminal not proved: %+v", r)
				}
				t.Logf("exchange_original_terminal ordinal=%d op=%s wait=%t tree=%t unknown=%t input=%s output=%s", r.Sequence, r.Operation, r.WaitProved, r.TreeProved, r.Unknown, r.InputSHA256, r.OutputSHA256)
			}
		}
		if s.State().Unknown || s.State().Failed || s.State().InFlight || b.State().Unknown || !b.State().EverStarted {
			t.Fatal("bad original terminal state")
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		if b.Binding() != (StreamBinding{ManifestSHA256: req.Grant.ManifestSHA256, ScopeSHA256: req.Grant.ScopeSHA256, ReservationID: req.Grant.ReservationID, AttemptID: req.Launch.AttemptID, PlanRevision: req.Launch.PlanRevision}) {
			t.Fatal("session closed original Broker")
		}
		var original bytes.Buffer
		if err = b.ReadSlot(ctx, 1, &original); err != nil || !bytes.Equal(original.Bytes(), canonical) {
			t.Fatalf("original Broker no longer readable: %v", err)
		}
		cleanup, err := b.Cleanup(ctx)
		if err != nil || cleanup == nil || cleanup.ScopeSHA256 != req.Grant.ScopeSHA256 {
			f.retain = true
			t.Fatalf("original cleanup: %+v %v", cleanup, err)
		}
		t.Logf("original_broker_cleanup bytes=%d unknown=%t", cleanup.RemovedBytes, b.State().Unknown)
	})
	t.Run("wrong_actual_input_never_starts", func(t *testing.T) {
		req := brokerRequest(t, f, "xor")
		b := liveBroker(t, ctx, f, req)
		defer func() {
			if b.State().Unknown {
				f.retain = true
			}
		}()
		p := []ExactTransformPermit{{Path: "file.txt", Operation: "clean", InputSHA256: exactHash([]byte("B")), InputBytes: 1, OutputSHA256: exactHash([]byte{0x18}), OutputBytes: 1, MaximumCalls: 1}}
		s, err := OpenExactTransformSession(b, b.Binding(), req.Manifest, p, &exactTestJournal{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		out, err := s.Exchange(ctx, 1, "file.txt", "clean", func(w io.Writer) error { _, err := w.Write([]byte("C")); return err })
		if err == nil || out != nil || !s.State().Failed || s.State().Unknown || b.State().EverStarted || b.State().Unknown {
			f.retain = true
			t.Fatalf("unauthorized input launched: session=%+v broker=%+v err=%v", s.State(), b.State(), err)
		}
		if _, err = b.Cleanup(ctx); err != nil {
			f.retain = true
			t.Fatal(err)
		}
		t.Log("wrong_input_original_broker_never_started=true cleanup=known")
	})
}
