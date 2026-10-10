//go:build darwin || linux

package gitfilter

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOriginalGitGateNativeHandle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path, err := filepath.EvalSymlinks("/bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	h, stream, err := process.StartDuplex(ctx, process.Command{Path: path, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin"}})
	if h != nil {
		t.Cleanup(func() {
			if stream != nil {
				if e := stream.Close(); e != nil {
					t.Errorf("stream Close: %v", e)
				}
			}
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if e := h.Stop(c); e != nil {
				t.Errorf("original Stop: %v", e)
			}
			if e := h.Wait(c); e != nil {
				t.Errorf("original Wait: %v", e)
			}
			if e := h.ConfirmTreeExited(); e != nil {
				t.Errorf("original tree: %v", e)
			}
		})
	}
	if err != nil || h == nil || stream == nil {
		t.Fatalf("original StartDuplex: %v", err)
	}
	copy := reflect.New(reflect.TypeOf(*h)).Interface().(*process.Handle)
	reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(h).Elem())
	for _, bad := range []*process.Handle{nil, new(process.Handle), copy} {
		g, _, _, _ := gateNew(t)
		if e := g.Activate(ctx, bad, g.digest); e == nil {
			t.Fatal("nonoriginal Handle activated gate")
		}
		if g.State().Activated || !g.State().Revoked {
			t.Fatalf("invalid Handle state=%+v", g.State())
		}
		_ = g.Close(gateCloseContext(t)) // known/unknown rejection, no endpoint
		if !g.State().Drained {
			t.Fatal("empty invalid gate not drained")
		}
	}
	g, s, b, _ := gateNew(t)
	if e := g.Activate(ctx, h, g.digest); e != nil {
		t.Fatal(e)
	}
	conn := gateMemory(gateWire("B", true))
	if e := g.Serve(conn, h.PID()); e != nil {
		t.Fatal(e)
	}
	if e := g.Close(gateCloseContext(t)); e != nil {
		t.Fatal(e)
	}
	if !g.State().Drained || g.State().ResponsesCommitted != 1 || b.closes != 0 || s.State().Closed {
		t.Fatalf("gate=%+v borrowed=%+v", g.State(), s.State())
	}
	if e := h.VerifyLiveDescendant(ctx, h.PID()); e != nil {
		t.Fatalf("gate revoked borrowed Git Handle: %v", e)
	}
	if e := stream.Close(); e != nil {
		t.Fatal(e)
	}
	if e := h.Wait(ctx); e != nil {
		t.Fatal(e)
	}
	if h.ExitCode() != 0 {
		t.Fatalf("original exit=%d", h.ExitCode())
	}
	if e := h.ConfirmTreeExited(); e != nil {
		t.Fatal(e)
	}
	late, _, _, _ := gateNew(t)
	if e := late.Activate(ctx, h, late.digest); e == nil {
		t.Fatal("waited Handle activated new gate")
	}
	_ = late.Close(gateCloseContext(t))
	t.Logf("original Git adapter fixture pid=%d original Wait exit=0 tree proved; caller peer PID is not socket authentication", h.PID())
}
