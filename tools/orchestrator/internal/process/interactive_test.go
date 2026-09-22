package process

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestOwnedInteractiveStdinClosesAtExplicitEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h, err := Start(ctx, Command{Path: "/bin/cat", StdinFile: r, Stdout: &output})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err = w.Write([]byte("owned request\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err = h.Wait(ctx); err != nil {
		h.Stop(context.Background())
		t.Fatal(err)
	}
	if output.String() != "owned request\n" || h.ExitCode() != 0 {
		t.Fatalf("interactive input lost: %q exit=%d", output.String(), h.ExitCode())
	}
	if err = h.ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
}
