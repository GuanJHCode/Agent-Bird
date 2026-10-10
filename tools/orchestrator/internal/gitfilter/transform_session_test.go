package gitfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func exactHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type exactTestBroker struct {
	binding                                    StreamBinding
	state                                      BrokerState
	slots                                      [3][]byte
	loads, transforms, reads, closes, cleanups int
	alterResult                                func(*Result)
	afterLoad                                  func()
	afterRead                                  func()
}

func (b *exactTestBroker) Binding() StreamBinding                    { return b.binding }
func (b *exactTestBroker) State() BrokerState                        { return b.state }
func (b *exactTestBroker) Load(context.Context, int, *os.File) error { panic("not stream loading") }
func (b *exactTestBroker) LoadStream(_ context.Context, slot int, produce func(io.Writer) error) (StreamLoadResult, error) {
	if slot != 0 {
		panic("caller-selected input slot")
	}
	b.loads++
	var out bytes.Buffer
	err := produce(&out)
	b.slots[slot] = append([]byte(nil), out.Bytes()...)
	if b.afterLoad != nil {
		b.afterLoad()
	}
	return StreamLoadResult{SHA256: exactHash(out.Bytes()), Bytes: int64(out.Len())}, err
}
func (b *exactTestBroker) Transform(_ context.Context, op string, src, dst int) (Result, error) {
	if src != 0 || dst != 1 || (op != "clean" && op != "smudge") {
		panic("caller-selected transform")
	}
	b.transforms++
	b.slots[1] = append([]byte(nil), b.slots[0]...)
	for i := range b.slots[1] {
		b.slots[1][i] ^= 0x5a
	}
	now := time.Now()
	r := Result{Accepted: true, Started: true, WaitProved: true, TreeProved: true, StartedAt: now, TerminalAt: now, ScopeSHA256: b.binding.ScopeSHA256, RequestSHA256: strings.Repeat("e", 64), InputSHA256: exactHash(b.slots[0]), OutputSHA256: exactHash(b.slots[1]), InputBytes: int64(len(b.slots[0])), OutputBytes: int64(len(b.slots[1]))}
	b.state.EverStarted = true
	if b.state.StartedAt.IsZero() {
		b.state.StartedAt = now
	}
	b.state.TerminalAt = now
	if b.alterResult != nil {
		b.alterResult(&r)
	}
	return r, nil
}
func (b *exactTestBroker) ReadSlot(_ context.Context, slot int, w io.Writer) error {
	if slot != 1 {
		panic("caller-selected output slot")
	}
	b.reads++
	_, err := w.Write(b.slots[1])
	if b.afterRead != nil {
		b.afterRead()
	}
	return err
}
func (b *exactTestBroker) Cleanup(context.Context) (*contract.GitFilterCleanup, error) {
	b.cleanups++
	return nil, nil
}
func (b *exactTestBroker) Close() error { b.closes++; return nil }

type exactTestJournal struct {
	records []ExactTransformRecord
	hook    func(ExactTransformRecord) error
}

func (j *exactTestJournal) RecordExactTransform(_ context.Context, r ExactTransformRecord) error {
	j.records = append(j.records, r)
	if j.hook != nil {
		return j.hook(r)
	}
	return nil
}
func exactTestFixture(t *testing.T) (*exactTestBroker, contract.GitFilterManifestV1, []ExactTransformPermit, *exactTestJournal, context.Context) {
	t.Helper()
	m := contract.GitFilterManifestV1{Version: 1, DriverID: "test", Paths: []string{"a.txt"}, Git: contract.GitFilterGitPin{Path: "/test/git", Version: "git-test", SHA256: strings.Repeat("a", 64)}, Clean: contract.GitFilterToolPin{Path: "/test/clean", SHA256: strings.Repeat("b", 64), Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: "/test/smudge", SHA256: strings.Repeat("c", 64), Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: strings.Repeat("d", 64)}
	d, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b := &exactTestBroker{binding: StreamBinding{ManifestSHA256: d, ScopeSHA256: strings.Repeat("f", 64), ReservationID: "reservation", AttemptID: "attempt", PlanRevision: 1}}
	in := []byte("B")
	out := []byte{in[0] ^ 0x5a}
	p := []ExactTransformPermit{{Path: "a.txt", Operation: "smudge", InputSHA256: exactHash(in), InputBytes: 1, OutputSHA256: exactHash(out), OutputBytes: 1, MaximumCalls: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return b, m, p, &exactTestJournal{}, ctx
}
func exactProduce(value string) func(io.Writer) error {
	return func(w io.Writer) error { _, err := io.WriteString(w, value); return err }
}
func openExactTest(t *testing.T, b *exactTestBroker, m contract.GitFilterManifestV1, p []ExactTransformPermit, j *exactTestJournal) *ExactTransformSession {
	t.Helper()
	s, e := OpenExactTransformSession(b, b.binding, m, p, j)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestExactTransformSession(t *testing.T) {
	t.Run("exact_copy_journal_and_descriptor_free_close", func(t *testing.T) {
		b, m, p, j, ctx := exactTestFixture(t)
		s := openExactTest(t, b, m, p, j)
		p[0].InputSHA256 = "changed"
		m.Paths[0] = "other"
		out, e := s.Exchange(ctx, 1, "a.txt", "smudge", exactProduce("B"))
		if e != nil || !bytes.Equal(out, []byte{0x18}) {
			t.Fatalf("output=%x err=%v", out, e)
		}
		if len(j.records) != 2 || j.records[0].Phase != "intent" || j.records[1].Phase != "terminal" || j.records[1].Sequence != 1 || j.records[1].InputSHA256 != exactHash([]byte("B")) || j.records[1].OutputSHA256 != exactHash(out) || !j.records[1].WaitProved || !j.records[1].TreeProved || j.records[0].PermitsSHA256 == "" {
			t.Fatalf("journal=%+v", j.records)
		}
		if e = s.Close(); e != nil || b.closes != 0 || b.cleanups != 0 || !s.State().Closed {
			t.Fatalf("close borrowed ownership: %v", e)
		}
		if out, e = s.Exchange(ctx, 2, "a.txt", "smudge", exactProduce("B")); e == nil || out != nil {
			t.Fatal("closed session reused")
		}
	})
	t.Run("constructor_refuses_binding_and_ambiguous_permits_without_io", func(t *testing.T) {
		b, m, p, j, _ := exactTestFixture(t)
		wrong := b.binding
		wrong.AttemptID = "foreign"
		if _, e := OpenExactTransformSession(b, wrong, m, p, j); e == nil {
			t.Fatal("foreign binding")
		}
		p = append(p, p[0])
		if _, e := OpenExactTransformSession(b, b.binding, m, p, j); e == nil {
			t.Fatal("duplicate input selector")
		}
		if b.loads+b.transforms+b.reads != 0 {
			t.Fatal("constructor performed IO")
		}
	})
	for _, kind := range []string{"path", "operation", "sequence", "input", "output", "scope", "wait", "tree", "timestamps", "unknown", "load_binding", "read_binding", "intent_failure", "terminal_failure", "terminal_drift", "cancelled", "missing_deadline", "exhausted", "replay"} {
		t.Run(kind, func(t *testing.T) {
			b, m, p, j, ctx := exactTestFixture(t)
			s := openExactTest(t, b, m, p, j)
			seq := uint64(1)
			path, op, input := "a.txt", "smudge", "B"
			switch kind {
			case "path":
				path = "foreign"
			case "operation":
				op = "exec"
			case "sequence":
				seq = 2
			case "input":
				input = "C"
			case "output":
				b.alterResult = func(r *Result) { r.OutputSHA256 = strings.Repeat("0", 64) }
			case "scope":
				b.alterResult = func(r *Result) { r.ScopeSHA256 = strings.Repeat("0", 64) }
			case "wait":
				b.alterResult = func(r *Result) { r.WaitProved = false }
			case "tree":
				b.alterResult = func(r *Result) { r.TreeProved = false }
			case "timestamps":
				b.alterResult = func(r *Result) { r.TerminalAt = time.Time{} }
			case "unknown":
				b.alterResult = func(r *Result) { r.Unknown = true }
			case "load_binding":
				b.afterLoad = func() { b.binding.AttemptID = "foreign" }
			case "read_binding":
				b.afterRead = func() { b.binding.AttemptID = "foreign" }
			case "intent_failure":
				j.hook = func(r ExactTransformRecord) error { return errors.New("journal failed") }
			case "terminal_failure":
				j.hook = func(r ExactTransformRecord) error {
					if r.Phase == "terminal" {
						return errors.New("journal failed")
					}
					return nil
				}
			case "terminal_drift":
				j.hook = func(r ExactTransformRecord) error {
					if r.Phase == "terminal" {
						b.binding.ScopeSHA256 = "foreign"
					}
					return nil
				}
			case "cancelled":
				c, stop := context.WithCancel(ctx)
				stop()
				ctx = c
			case "missing_deadline":
				ctx = context.Background()
			case "exhausted", "replay":
				if _, e := s.Exchange(ctx, 1, path, op, exactProduce(input)); e != nil {
					t.Fatal(e)
				}
				seq = 2
				if kind == "replay" {
					seq = 1
				}
			}
			before := b.transforms
			out, e := s.Exchange(ctx, seq, path, op, exactProduce(input))
			if e == nil || out != nil || !s.State().Failed {
				t.Fatalf("accepted %s: out=%x err=%v state=%+v", kind, out, e, s.State())
			}
			if kind == "input" && b.transforms != 0 {
				t.Fatal("wrong input started tool")
			}
			switch kind {
			case "path", "operation", "sequence", "intent_failure", "cancelled", "missing_deadline", "exhausted", "replay":
				if b.transforms != before {
					t.Fatal("preflight failure started tool")
				}
			}
			switch kind {
			case "wait", "tree", "timestamps", "unknown", "terminal_failure":
				if !s.State().Unknown {
					t.Fatal("missing uncertainty")
				}
			}
			calls := b.transforms
			if _, e = s.Exchange(ctx, seq+1, "a.txt", "smudge", exactProduce("B")); e == nil || b.transforms != calls {
				t.Fatal("failed session reused")
			}
		})
	}
	t.Run("concurrent_request_revokes_active_output", func(t *testing.T) {
		b, m, p, j, ctx := exactTestFixture(t)
		s := openExactTest(t, b, m, p, j)
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			out, e := s.Exchange(ctx, 1, "a.txt", "smudge", func(w io.Writer) error { close(entered); <-release; return exactProduce("B")(w) })
			if out != nil {
				e = errors.New("revoked output published")
			}
			done <- e
		}()
		<-entered
		if _, e := s.Exchange(ctx, 2, "a.txt", "smudge", exactProduce("B")); e == nil {
			t.Error("concurrent request accepted")
		}
		close(release)
		if e := <-done; e == nil {
			t.Fatal("active request not revoked")
		}
		if !s.State().Failed || s.State().InFlight {
			t.Fatal("bad terminal state")
		}
	})
}
