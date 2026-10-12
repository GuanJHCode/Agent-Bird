package process

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Pure metadata fixture: no actor, Start, Stop, Wait or process-tree evidence.
func TestDuplexLaunchStateViewCannotMutateOriginalMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	spawned := time.Now()
	completed := spawned.Add(time.Millisecond)
	original := DuplexStartupCleanup{Context: ctx, Deadline: deadline, Result: ErrProcessTreeUnknown, CompletedAt: completed}
	h := &Handle{duplexLaunch: &DuplexLaunchState{SpawnedAt: spawned, StartupCleanup: &original}}
	view, ok := h.DuplexLaunchState()
	if !ok || view.SpawnedAt != spawned || view.StartupCleanup == nil || view.StartupCleanup.Context != ctx || view.StartupCleanup.Deadline != deadline || view.StartupCleanup.CompletedAt != completed || view.StartupCleanup.Result != ErrProcessTreeUnknown {
		t.Fatal("original startup cleanup metadata missing")
	}
	view.SpawnedAt = time.Time{}
	view.StartupCleanup.Result = nil
	view.StartupCleanup.Context = context.Background()
	view.StartupCleanup.CompletedAt = time.Time{}
	view.StartupCleanup.Deadline = time.Time{}
	fresh, ok := h.DuplexLaunchState()
	if !ok || fresh.SpawnedAt != spawned || fresh.StartupCleanup == nil || *fresh.StartupCleanup != original {
		t.Fatal("copied API view mutated original first cleanup result/window")
	}
	cancel()
	fresh, ok = h.DuplexLaunchState()
	if !ok || fresh.StartupCleanup.Context.Err() != context.Canceled || fresh.StartupCleanup.Result != ErrProcessTreeUnknown || fresh.StartupCleanup.Deadline != deadline {
		t.Fatal("original cancel erased first cleanup facts")
	}
	raw, err := json.Marshal(fresh)
	if err != nil || string(raw) != "{}" {
		t.Fatal("internal launch facts became a wire payload")
	}
}

func TestPlainOrNilHandleHasNoDuplexLaunchState(t *testing.T) {
	for _, h := range []*Handle{nil, {}} {
		if _, ok := h.DuplexLaunchState(); ok {
			t.Fatal("plain/nil handle produced duplex observations")
		}
	}
	var fromJSON Handle
	if err := json.Unmarshal([]byte(`{"duplexLaunch":{"SpawnedAt":"forged"}}`), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if _, ok := fromJSON.DuplexLaunchState(); ok {
		t.Fatal("wire input constructed private launch observations")
	}
}
