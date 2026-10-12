package gitfilter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

func fpkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
func TestRunFilterProcessGoldenClean(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	in := fpkt("git-filter-client\n") + fpkt("version=2\n") + "0000" + fpkt("capability=clean\n") + "0000" + fpkt("command=clean\n") + fpkt("pathname=a\n") + "0000" + fpkt("abc") + "0000"
	var out bytes.Buffer
	err := RunFilterProcess(ctx, bytes.NewBufferString(in), &out, func(_ context.Context, seq uint64, path, op string, produce func(io.Writer) error) ([]byte, error) {
		if seq != 1 || path != "a" || op != "clean" {
			t.Fatalf("request %d %q %q", seq, path, op)
		}
		var b bytes.Buffer
		if err := produce(&b); err != nil || b.String() != "abc" {
			t.Fatalf("produce %q %v", b.String(), err)
		}
		return []byte("ABC"), nil
	})
	if err != nil {
		t.Fatalf("RunFilterProcess: %v", err)
	}
	want := fpkt("git-filter-server\n") + fpkt("version=2\n") + "0000" + fpkt("capability=clean\n") + "0000" + fpkt("status=success\n") + "0000" + fpkt("ABC") + "0000" + "0000"
	if out.String() != want {
		t.Fatalf("output=%q want=%q", out.String(), want)
	}
}
