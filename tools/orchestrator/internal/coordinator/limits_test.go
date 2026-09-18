package coordinator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupRejectsInvalidOrPublicConcurrencyPolicy(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "limits-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{{"invalid", `{"version":1,"global":0}`, 0600}, {"public", `{"version":1,"global":3}`, 0644}} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(root, tc.name)
			if err = os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(state, "runtime-limits.json"), []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			server, err := NewServer(state)
			if err == nil {
				server.Close()
				t.Fatal("untrusted limits silently ignored")
			}
		})
	}
}
