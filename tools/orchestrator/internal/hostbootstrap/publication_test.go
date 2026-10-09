//go:build !windows

package hostbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publicationFixture(t *testing.T) (*Lease, []byte, []byte, MetadataV2, string) {
	t.Helper()
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(state, "host-bootstrap"), 0700); e != nil {
		t.Fatal(e)
	}
	m := MetadataV2{Version: 2, OwnerLaunchProtocol: 2, SocketPath: filepath.Join(state, "coordinator.sock"), SpoolRoot: filepath.Join(state, "host-spool", "launch"), ProducerID: "launch", Hello: HelloMetadata{LaunchID: "launch", OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "old", PID: 101, Birth: "host-birth", Executable: "/private/fixture/orchestrator"}}
	prior, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	prior = append(prior, '\n')
	if e = os.WriteFile(filepath.Join(state, "host-bootstrap", "launch.json"), prior, 0600); e != nil {
		t.Fatal(e)
	}
	l, e := Open(context.Background(), state, "launch")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(l.Close)
	if _, e = l.Read(); e != nil {
		t.Fatal(e)
	}
	m.Hello.HostGeneration = "next"
	expected, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	expected = append(expected, '\n')
	return l, prior, expected, m, state
}
func TestPublicationLegacyStageRefusal(t *testing.T) {
	for _, name := range []string{".launch.json.replace-" + strings.Repeat("a", 32), ".publication-" + strings.Repeat("a", 64), ".publication-unknown"} {
		t.Run(name, func(t *testing.T) {
			l, prior, expected, m, state := publicationFixture(t)
			stage := filepath.Join(state, "host-bootstrap", name)
			if e := os.WriteFile(stage, expected, 0600); e != nil {
				t.Fatal(e)
			}
			if e := l.PublishStable(m); e == nil {
				t.Fatal("fresh/unowned stage silently adopted/ignored")
			}
			final, e := os.ReadFile(filepath.Join(state, "host-bootstrap", "launch.json"))
			if e != nil || !bytes.Equal(final, prior) {
				t.Fatal("refusal changed final", e)
			}
			old, e := os.ReadFile(stage)
			if e != nil || !bytes.Equal(old, expected) {
				t.Fatal("refusal erased evidence", e)
			}
		})
	}
}

func TestPublicationFrozenBytesAndExactReplay(t *testing.T) {
	l, prior, expected, _, state := publicationFixture(t)
	expected = append(expected, ' ', '\n')
	original := append([]byte(nil), expected...)
	snapshot, e := l.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	snapshot[0] = '!'
	if again, e := l.Snapshot(); e != nil || !bytes.Equal(again, prior) {
		t.Fatal("snapshot aliases mutable bytes", e)
	}
	if e = l.PublishPublication(expected, publicationHash(prior)); e == nil {
		t.Fatal("unprepared publisher admitted")
	}
	if e = l.PreparePublication(expected, false); e != nil {
		t.Fatal(e)
	}
	if e = l.publishPublication(expected, publicationHash(prior), func(point string) error {
		if point == "created" {
			expected[0] = '!'
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	body, e := os.ReadFile(filepath.Join(state, "host-bootstrap", "launch.json"))
	if e != nil || !bytes.Equal(body, original) {
		t.Fatal("frozen publication was re-marshaled or aliased", e)
	}
	if e = l.VerifyPublication(original); e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(filepath.Join(state, "host-bootstrap", "launch.json"))
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
	replay, e := Open(context.Background(), state, "launch")
	if e != nil {
		t.Fatal(e)
	}
	defer replay.Close()
	if _, e = replay.Read(); e != nil {
		t.Fatal(e)
	}
	if e = replay.PreparePublication(original, true); e != nil {
		t.Fatal(e)
	}
	if e = replay.PublishPublication(original, publicationHash(prior)); e != nil {
		t.Fatal(e)
	}
	after, e := os.Stat(filepath.Join(state, "host-bootstrap", "launch.json"))
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("already-expected replay replaced its inode", e)
	}
	names, e := os.ReadDir(filepath.Join(state, "host-bootstrap"))
	if e != nil || len(names) != 1 {
		t.Fatal("unexpected stage", names, e)
	}
	stage := filepath.Join(state, "host-bootstrap", replay.publicationName(original))
	if e = os.WriteFile(stage, original, 0600); e != nil {
		t.Fatal(e)
	}
	if e = replay.VerifyPublication(original); e == nil {
		t.Fatal("expected+stage receipt admitted")
	}
	if e = replay.PreparePublication(original, true); e == nil {
		t.Fatal("expected+stage replay admitted")
	}
	if e = replay.PublishPublication(original, publicationHash(prior)); e == nil {
		t.Fatal("expected+stage publication admitted")
	}
	if _, e = os.Stat(stage); e != nil {
		t.Fatal("expected+stage evidence erased", e)
	}
}
func TestPublicationStageDriftAndPrefixRejection(t *testing.T) {
	for _, kind := range []string{"fresh_existing", "wrong_prefix", "oversize", "hardlink", "symlink", "grow_after_stat", "mode_drift", "inode_drift", "final_drift", "wrong_prior"} {
		t.Run(kind, func(t *testing.T) {
			l, prior, expected, _, state := publicationFixture(t)
			stage := filepath.Join(state, "host-bootstrap", l.publicationName(expected))
			final := filepath.Join(state, "host-bootstrap", "launch.json")
			if e := os.WriteFile(stage, expected[:len(expected)/2], 0600); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "wrong_prefix":
				if e := os.WriteFile(stage, []byte("not-prefix"), 0600); e != nil {
					t.Fatal(e)
				}
			case "oversize":
				if e := os.WriteFile(stage, append(append([]byte(nil), expected...), 1), 0600); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(stage, stage+"-link"); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Remove(stage); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(final, stage); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "fresh_existing" || kind == "wrong_prefix" || kind == "oversize" || kind == "hardlink" || kind == "symlink" {
				if e := l.PreparePublication(expected, kind != "fresh_existing"); e == nil {
					t.Fatal("bad stage accepted", kind)
				}
			} else {
				if e := l.PreparePublication(expected, true); e != nil {
					t.Fatal(e)
				}
				priorHash := publicationHash(prior)
				if kind == "wrong_prior" {
					priorHash = strings.Repeat("f", 64)
				}
				changed := false
				e := l.publishPublication(expected, priorHash, func(point string) error {
					if !changed && point == "stage_stated" {
						changed = true
						switch kind {
						case "grow_after_stat":
							return os.WriteFile(stage, append(append([]byte(nil), expected...), 1), 0600)
						case "mode_drift":
							return os.Chmod(stage, 0644)
						case "inode_drift":
							if e := os.Rename(stage, stage+"-old"); e != nil {
								return e
							}
							return os.WriteFile(stage, expected[:len(expected)/2], 0600)
						case "final_drift":
							return os.WriteFile(final, []byte("foreign"), 0600)
						}
					}
					return nil
				})
				if e == nil {
					t.Fatal("drift was published", kind)
				}
			}
			if _, e := os.Lstat(stage); e != nil {
				t.Fatal("old replay evidence removed", kind, e)
			}
			if kind != "final_drift" {
				body, e := os.ReadFile(final)
				if e != nil || !bytes.Equal(body, prior) {
					t.Fatal("failure changed final", kind, e)
				}
			}
		})
	}
}
