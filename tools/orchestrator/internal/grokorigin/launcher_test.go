//go:build !windows

package grokorigin

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestOriginVersionCaptureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, version, stdout, stderr string
		want                          bool
	}{
		{"plain", "version", "version", "", true},
		{"newline", "version", "version\n", "", true},
		{"max", strings.Repeat("v", 256), strings.Repeat("v", 256) + "\n", "", true},
		{"empty", "version", "", "", false},
		{"extra_newline", "version", "version\n\n", "", false},
		{"stderr", "version", "version\n", "x", false},
		{"invalid_utf8", "version", "version\xff", "", false},
		{"overflow", "version", "version" + strings.Repeat("x", 4096), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c originVersionCapture
			for _, b := range []byte(tc.stdout) {
				c.observe(process.OutputStdout, []byte{b})
			}
			c.observe(process.OutputStderr, []byte(tc.stderr))
			if c.matches(tc.version) != tc.want || len(c.stdout) > 257 {
				t.Fatal("version boundary mismatch")
			}
		})
	}
}

func TestOriginPreparedFormattingNeverIncludesEnvironment(t *testing.T) {
	p := &Prepared{env: []string{"XAI_API_KEY=synthetic-private-key"}, policy: "private-policy", request: Request{InitialPrompt: "private-prompt"}}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, p); got != "grokorigin.Prepared" {
			t.Fatalf("unexpected diagnostic: %q", got)
		}
	}
}

func TestOriginPrepareRejectsInvalidOrUnsupportedInput(t *testing.T) {
	if p, e := Prepare(context.Background(), "", Request{}); e == nil || p != nil {
		t.Fatal("invalid request accepted")
	}
	if p, e := Prepare(nil, "", Request{}); e == nil || p != nil {
		t.Fatal("nil context accepted")
	}
	if runtime.GOOS != "darwin" {
		r := Request{SchemaVersion: 1, Binary: "/fixture", BinarySHA256: strings.Repeat("a", 64), BinaryVersion: "fixture", Workspace: "/workspace", InitialPrompt: "fixture", OperationTimeoutMS: 100, TotalTimeoutMS: 1000, OriginProfile: "readonly", AuthMode: "none"}
		if p, e := Prepare(context.Background(), "/state", r); p != nil || e == nil || e.Error() != "grokorigin_unsupported_platform" {
			t.Fatalf("platform gate=%v", e)
		}
	}
}
