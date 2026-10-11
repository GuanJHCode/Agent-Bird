//go:build darwin

package gitfilter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type carrierSocketTestJournal struct {
	records []carrierSocketRecord
	hook    func(carrierSocketRecord) error
}

func (j *carrierSocketTestJournal) RecordCarrierSocket(_ context.Context, r carrierSocketRecord) error {
	j.records = append(j.records, r)
	if j.hook != nil {
		return j.hook(r)
	}
	return nil
}
func carrierTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func carrierTestBinding() carrierSocketBinding {
	return carrierSocketBinding{Original: StreamBinding{ManifestSHA256: "m", ScopeSHA256: "s", ReservationID: "r", AttemptID: "a", PlanRevision: 1}, ReservationID: "carrier", Scope: "scope", RuntimeNamespace: "darwin-private-tmp-v1", DirectorySlots: 1, SocketSlots: 1}
}
func carrierTestSegment(t *testing.T) string {
	t.Helper()
	segment := t.TempDir()
	if err := os.Chmod(segment, 0700); err != nil {
		t.Fatal(err)
	}
	return segment
}

// Every path here comes from this test's reserved record. Unknown production
// owners are never scanned or cleaned. Close original listener/FDs before removal.
func carrierTestRelease(t *testing.T, o *carrierSocket, paths ...string) {
	t.Helper()
	if o != nil {
		s := o.state
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if !s.removed {
			_ = s.closeFDs()
		}
		paths = append(paths, s.directory)
	}
	for _, p := range paths {
		if p != "" {
			if err := os.RemoveAll(p); err != nil {
				t.Error(err)
			}
		}
	}
}
func carrierTestUnknown(t *testing.T, o *carrierSocket) {
	t.Helper()
	if o == nil {
		t.Fatal("lost original owner after journal attempt")
	}
	o.state.mu.Lock()
	unknown := o.state.unknown
	o.state.mu.Unlock()
	if !unknown {
		t.Fatal("uncertainty was not sticky")
	}
	if err := o.remove(carrierTestContext(t)); err == nil {
		t.Fatal("Unknown removal admitted")
	}
}
func TestCarrierSocketNativeLifecycle(t *testing.T) {
	segment := filepath.Join(carrierTestSegment(t), strings.Repeat("long-segment-", 15))
	if err := os.Mkdir(segment, 0700); err != nil {
		t.Fatal(err)
	}
	borrowed := filepath.Join(segment, "borrowed")
	if err := os.WriteFile(borrowed, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := named(segment)
	if err != nil {
		t.Fatal(err)
	}
	ctx := carrierTestContext(t)
	j := &carrierSocketTestJournal{}
	j.hook = func(r carrierSocketRecord) error {
		switch r.Phase {
		case "reserved":
			if _, e := os.Lstat(r.Directory); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("reserved after creation: %v", e)
			}
		case "prepared":
			info, e := os.Lstat(r.Socket)
			if e != nil || info.Mode() != os.ModeSocket|0600 || r.SocketID.Inode == 0 {
				t.Fatalf("prepared without frozen 0600 socket: %v", e)
			}
		case "removed":
			if _, e := os.Lstat(r.Directory); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("removed before absence: %v", e)
			}
		}
		return nil
	}
	o, err := newCarrierSocket(ctx, segment, carrierTestBinding(), j)
	defer func() { carrierTestRelease(t, o) }()
	if err != nil || o == nil {
		t.Fatalf("create owner=%t err=%v", o != nil, err)
	}
	if len(j.records) != 2 || j.records[0].Phase != "reserved" || j.records[1].Phase != "prepared" {
		t.Fatalf("journal=%+v", j.records)
	}
	if len(o.state.socket) >= 104 || filepath.Dir(o.state.directory) != "/private/tmp" {
		t.Fatal("socket not in fixed short namespace")
	}
	info, err := os.Lstat(o.state.directory)
	if err != nil || info.Mode() != os.ModeDir|0700 {
		t.Fatalf("directory: %v %v", info, err)
	}
	if err := o.verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := o.remove(ctx); err == nil {
		t.Fatal("removal before closure admitted")
	}
	if err := o.close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(o.state.socket); err != nil {
		t.Fatalf("Close deleted socket: %v", err)
	}
	if err := o.remove(ctx); err != nil {
		t.Fatal(err)
	}
	if len(j.records) != 4 || j.records[2].Phase != "removing" || j.records[3].Phase != "removed" {
		t.Fatalf("journal=%+v", j.records)
	}
	after, err := named(segment)
	if err != nil || before != after {
		t.Fatalf("borrowed segment changed: %v", err)
	}
	body, err := os.ReadFile(borrowed)
	if err != nil || string(body) != "preserve" {
		t.Fatalf("borrowed entry changed: %v", err)
	}
}
func TestCarrierSocketReservedBoundaries(t *testing.T) {
	for _, scenario := range []string{"journal_error", "cancel", "collision", "segment_replacement"} {
		t.Run(scenario, func(t *testing.T) {
			segment := carrierTestSegment(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			j := &carrierSocketTestJournal{}
			fired := 0
			var owned string
			j.hook = func(r carrierSocketRecord) error {
				if r.Phase != "reserved" {
					return nil
				}
				fired++
				owned = r.Directory
				if _, e := os.Lstat(r.Directory); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("bad positive boundary: %v", e)
				}
				switch scenario {
				case "journal_error":
					return errors.New("durable-but-error")
				case "cancel":
					cancel()
				case "collision":
					if e := os.Mkdir(r.Directory, 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(r.Directory, "foreign"), []byte("foreign"), 0600); e != nil {
						t.Fatal(e)
					}
				case "segment_replacement":
					if e := os.Rename(segment, segment+"-original"); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(segment, 0700); e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() { _ = os.RemoveAll(segment + "-original") })
				}
				return nil
			}
			o, err := newCarrierSocket(ctx, segment, carrierTestBinding(), j)
			defer func() { carrierTestRelease(t, o, owned) }()
			if fired != 1 || err == nil || o == nil {
				t.Fatalf("fired=%d owner=%t err=%v", fired, o != nil, err)
			}
			if scenario == "collision" {
				if len(j.records) != 2 || j.records[1].Phase != "not_created" || !o.state.notCreated || o.state.unknown {
					t.Fatalf("collision state=%+v records=%+v", o.state, j.records)
				}
				body, e := os.ReadFile(filepath.Join(owned, "foreign"))
				if e != nil || string(body) != "foreign" {
					t.Fatal("foreign entry altered")
				}
				if e := o.close(carrierTestContext(t)); e != nil {
					t.Fatal(e)
				}
			} else {
				carrierTestUnknown(t, o)
				if _, e := os.Lstat(owned); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("created after reserved failure: %v", e)
				}
			}
		})
	}
}
func TestCarrierSocketCallbackDrift(t *testing.T) {
	for _, scenario := range []string{"prepared_error", "prepared_inventory", "prepared_symlink", "removing_inventory", "removed_rebind"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := carrierTestContext(t)
			segment := carrierTestSegment(t)
			j := &carrierSocketTestJournal{}
			fired := 0
			j.hook = func(r carrierSocketRecord) error {
				phase := strings.Split(scenario, "_")[0]
				if r.Phase != phase {
					return nil
				}
				fired++
				if phase != "removed" {
					if _, e := os.Lstat(r.Socket); e != nil {
						t.Fatalf("did not reach socket boundary: %v", e)
					}
				}
				switch scenario {
				case "prepared_error":
					return errors.New("prepared journal uncertain")
				case "prepared_inventory", "removing_inventory":
					if e := os.WriteFile(filepath.Join(r.Directory, "foreign"), []byte("foreign"), 0600); e != nil {
						t.Fatal(e)
					}
				case "prepared_symlink":
					if e := os.Remove(r.Socket); e != nil {
						t.Fatal(e)
					}
					if e := os.Symlink(segment, r.Socket); e != nil {
						t.Fatal(e)
					}
				case "removed_rebind":
					if e := os.Mkdir(r.Directory, 0700); e != nil {
						t.Fatal(e)
					}
				}
				return nil
			}
			o, err := newCarrierSocket(ctx, segment, carrierTestBinding(), j)
			defer func() { carrierTestRelease(t, o) }()
			if strings.HasPrefix(scenario, "prepared") {
				if err == nil {
					t.Fatal("prepared drift accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := o.close(ctx); err != nil {
					t.Fatal(err)
				}
				if err := o.remove(ctx); err == nil {
					t.Fatal("delete callback drift accepted")
				}
			}
			if fired != 1 {
				t.Fatalf("callback fired=%d", fired)
			}
			carrierTestUnknown(t, o)
			if scenario == "removing_inventory" {
				if _, e := os.Lstat(o.state.socket); e != nil {
					t.Fatalf("deleted before revalidation: %v", e)
				}
			}
		})
	}
}
func TestCarrierSocketOriginalAndConcurrent(t *testing.T) {
	ctx := carrierTestContext(t)
	j := &carrierSocketTestJournal{}
	o, err := newCarrierSocket(ctx, carrierTestSegment(t), carrierTestBinding(), j)
	defer func() { carrierTestRelease(t, o) }()
	if err != nil {
		t.Fatal(err)
	}
	copyOwner := *o
	for _, foreign := range []*carrierSocket{nil, {}, &copyOwner} {
		if foreign.verify(ctx) == nil || foreign.close(ctx) == nil || foreign.remove(ctx) == nil {
			t.Fatal("nonoriginal owner admitted")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = o.verify(ctx) }()
	}
	wg.Wait()
	if err := o.verify(ctx); err != nil {
		t.Fatalf("concurrent checks corrupted owner: %v", err)
	}
	if err := o.close(ctx); err != nil {
		t.Fatal(err)
	}
	if o.close(ctx) == nil || o.verify(ctx) == nil {
		t.Fatal("closed owner usable")
	}
	if err := o.remove(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestCarrierSocketRejectBeforeMutation(t *testing.T) {
	segment := carrierTestSegment(t)
	j := &carrierSocketTestJournal{}
	if o, e := newCarrierSocket(context.Background(), segment, carrierTestBinding(), j); e == nil || o != nil {
		t.Fatal("unbounded context admitted")
	}
	if err := os.Chmod(segment, 0755); err != nil {
		t.Fatal(err)
	}
	if o, e := newCarrierSocket(carrierTestContext(t), segment, carrierTestBinding(), j); e == nil || o != nil {
		t.Fatal("nonprivate segment admitted")
	}
	if len(j.records) != 0 {
		t.Fatal("invalid input attempted journal")
	}
}
