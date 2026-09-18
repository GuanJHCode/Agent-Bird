package adapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGrokNativeAuthPreflight(t *testing.T) {
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pin := BinaryPin{Path: path, Version: "grok 1.0.34 (3736acbc8658)", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	for _, mode := range []string{"success", "fail", "slow", "bad-pin", "start-failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_GROK_AUTH_HELPER", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if mode == "slow" {
				ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
			}
			candidate := pin
			if mode == "bad-pin" {
				candidate.SHA256 = strings.Repeat("0", 64)
			}
			if mode == "start-failed" {
				path := filepath.Join(t.TempDir(), "invalid-native")
				body := []byte("invalid executable format")
				if err := os.WriteFile(path, body, 0700); err != nil {
					t.Fatal(err)
				}
				candidate.Path, _ = filepath.EvalSymlinks(path)
				candidate.SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
			}
			err := RefreshGrokAuth(ctx, candidate, nil)
			if mode == "start-failed" && !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("lost startup uncertainty: %v", err)
			}
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			if mode != "success" && err == nil {
				t.Fatal("invalid preflight accepted")
			}
			if err != nil && strings.Contains(err.Error(), "private-auth-detail") {
				t.Fatal("auth output leaked")
			}
		})
	}
}
