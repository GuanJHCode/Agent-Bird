//go:build !windows

package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnedConfigAbandonTerminalBlocksLatePreparation(t *testing.T) {
	for _, phase := range []string{"terminal", "pending-terminal", "malformed-terminal"} {
		t.Run(phase, func(t *testing.T) {
			dest, exe := ownedConfigPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			a := ownedConfigPublish(t, exe, scope, "a", nil, "a")
			use, e := ownedConfigBegin(t, exe, scope, "run", []string{"task"}, strings.Repeat("1", 64))
			if e != nil {
				t.Fatal(e)
			}
			hash := strings.Repeat("2", 64)
			if e = PinConfigPreparation(exe, use, hash); e != nil {
				t.Fatal(e)
			}
			key := publicationBytesDigest([]byte("run"))
			dir := filepath.Join(dest, "owned-config", scope, "abandoned", key[:2])
			if e = os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			name := key + ".terminal.json"
			if phase == "pending-terminal" {
				name = ".pending-" + name
			}
			// Any terminal existence must block late entry, even invalid/pending metadata.
			if e = os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = ownedConfigBegin(t, exe, scope, "run", []string{"task"}, use.RequestSHA256); e == nil {
				t.Error("terminal ignored by Begin")
			}
			if e = PinConfigPreparation(exe, use, hash); e == nil {
				t.Error("terminal ignored by Pin")
			}
			raw, _ := json.Marshal(map[string]any{"kind": "fake", "provider": "antigravity-cli", "profile": map[string]any{"model": "a"}, "config_generation": a.Ref, "config_model_source": "saved-default"})
			if e = ValidateConfigGenerationSubmission(exe, scope, "run", hash, []ConfigGenerationTaskUse{{TaskID: "task", AdapterPayload: raw}}); e == nil {
				t.Error("terminal ignored by receiver validation")
			}
			if _, e = os.Stat(filepath.Join(dest, "pins", "task.json")); e != nil {
				t.Fatal("reference lost", e)
			}
		})
	}
}

// These FS-only tests supply a trusted synthetic CPP digest and fence, not
// authentication/Store commit proof. Real Store/CPP behavior is in cmd tests.
func abandonFSFixture(t *testing.T) (string, string, string, ConfigPreparationUse, string) {
	t.Helper()
	dest, exe := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	ownedConfigPublish(t, exe, scope, "a", nil, "a")
	u, e := ownedConfigBegin(t, exe, scope, "run", []string{"task-a", "task-b"}, strings.Repeat("1", 64))
	if e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("2", 64)
	return dest, exe, scope, u, digest
}
func abandonFSGuard(t *testing.T, exe, scope string, u ConfigPreparationUse, digest string, cpp bool, fence *ConfigAbandonFence) *ConfigAbandonGuard {
	t.Helper()
	g, e := OpenConfigAbandonGuard(exe, scope, u.RunID, "", fence)
	if e != nil {
		t.Fatal(e)
	}
	if !g.Completed() {
		if e = g.InspectCleanup(func(ConfigPreparationUse) (string, bool, error) { return digest, cpp, nil }); e != nil {
			g.Close()
			t.Fatal(e)
		}
	}
	return g
}
func replaceAbandonFile(t *testing.T, path string) {
	t.Helper()
	body, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(path, path+".original"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestOwnedConfigAbandonRecordIdentityBarrier(t *testing.T) {
	for _, kind := range []string{"terminal-before-delete", "history-before-delete", "complete-before-return", "completed-replay"} {
		t.Run(kind, func(t *testing.T) {
			dest, exe, scope, u, digest := abandonFSFixture(t)
			if e := PinConfigPreparation(exe, u, digest); e != nil {
				t.Fatal(e)
			}
			g := abandonFSGuard(t, exe, scope, u, digest, true, nil)
			defer g.Close()
			f, e := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
			if e != nil {
				t.Fatal(e)
			}
			key := publicationBytesDigest([]byte(u.RunID))
			dir := filepath.Join(dest, "owned-config", scope, "abandoned", key[:2])
			if kind == "completed-replay" {
				if _, e = g.Finish(f); e != nil {
					t.Fatal(e)
				}
				g.Close()
				g = abandonFSGuard(t, exe, scope, u, digest, true, &f)
				defer g.Close()
				replaceAbandonFile(t, filepath.Join(dir, key+".complete.json"))
				if _, e = g.Finish(f); e == nil {
					t.Fatal("completed replay accepted changed original completion identity")
				}
				return
			}
			_, e = g.finish(f, func(point string) error {
				if kind == "terminal-before-delete" && point == "terminal" {
					replaceAbandonFile(t, filepath.Join(dir, key+".terminal.json"))
				}
				if kind == "history-before-delete" && point == "terminal" {
					replaceAbandonFile(t, filepath.Join(dest, "owned-config", scope, "uses", key[:2], key+".json"))
				}
				if kind == "complete-before-return" && point == "complete" {
					replaceAbandonFile(t, filepath.Join(dir, key+".complete.json"))
				}
				return nil
			})
			if e == nil {
				t.Error("same-byte record replacement was accepted", kind)
			}
			if strings.HasSuffix(kind, "before-delete") {
				for _, id := range u.TaskIDs {
					if _, x := os.Stat(filepath.Join(dest, "pins", id+".json")); x != nil {
						t.Error("unproved terminal/history removed original reference", id, x)
					}
				}
			}
		})
	}
}
func TestOwnedConfigAbandonDurableWindows(t *testing.T) {
	for _, point := range []string{"before-terminal", "terminal", "pin-removed:task-a", "before-complete", "complete"} {
		t.Run(point, func(t *testing.T) {
			dest, exe, scope, u, digest := abandonFSFixture(t)
			if e := PinConfigPreparation(exe, u, digest); e != nil {
				t.Fatal(e)
			}
			g := abandonFSGuard(t, exe, scope, u, digest, true, nil)
			f, _ := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
			stop := errors.New("original-fs-window")
			if _, e := g.finish(f, func(p string) error {
				if p == point {
					return stop
				}
				return nil
			}); !errors.Is(e, stop) {
				t.Fatal("window not reached", e)
			}
			g.Close()
			next := abandonFSGuard(t, exe, scope, u, digest, true, &f)
			defer next.Close()
			if _, e := next.Finish(f); e != nil {
				t.Fatal("window did not recover", e)
			}
			for _, id := range u.TaskIDs {
				if _, e := os.Stat(filepath.Join(dest, "pins", id+".json")); !os.IsNotExist(e) {
					t.Fatal("unremoved original pin", e)
				}
			}
		})
	}
}

func TestOwnedConfigAbandonCurrentWitnessShape(t *testing.T) {
	for _, kind := range []string{"partial", "missing-cpp", "foreign-pin", "foreign-hold"} {
		t.Run(kind, func(t *testing.T) {
			dest, exe, scope, u, digest := abandonFSFixture(t)
			if e := PinConfigPreparation(exe, u, digest); e != nil {
				t.Fatal(e)
			}
			key := publicationBytesDigest([]byte(u.RunID))
			hold := filepath.Join(dest, "owned-config", scope, "preparations", key+".json")
			if kind == "partial" || kind == "foreign-hold" {
				original := u
				if kind == "foreign-hold" {
					original.RequestSHA256 = strings.Repeat("e", 64)
				}
				body, _ := json.Marshal(original)
				if e := os.WriteFile(hold, append(body, '\n'), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "partial" {
				if e := os.Remove(filepath.Join(dest, "pins", "task-b.json")); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "foreign-pin" {
				path := filepath.Join(dest, "pins", "task-a.json")
				body, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				p, e := generationDecodePinRecord(body)
				if e != nil {
					t.Fatal(e)
				}
				p.RunID = "foreign-run"
				body, _ = json.Marshal(p)
				if e = os.WriteFile(path, append(body, '\n'), 0600); e != nil {
					t.Fatal(e)
				}
			}
			g, e := OpenConfigAbandonGuard(exe, scope, u.RunID, "", nil)
			if e == nil {
				defer g.Close()
				e = g.InspectCleanup(func(ConfigPreparationUse) (string, bool, error) { return digest, kind != "missing-cpp", nil })
			}
			if kind == "partial" {
				if e != nil {
					t.Fatal("valid partial witness rejected", e)
				}
				f, _ := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
				if _, e = g.Finish(f); e != nil {
					t.Fatal(e)
				}
				return
			}
			if e == nil {
				t.Fatal("ambiguous/foreign witness admitted", kind)
			}
			if _, e := os.Lstat(filepath.Join(dest, "pins", "task-a.json")); e != nil {
				t.Fatal("rejected witness removed reference", e)
			}
		})
	}
}

func TestOwnedConfigAbandonValidTerminalBlocksOnlyOriginalRun(t *testing.T) {
	_, exe, scope, u, digest := abandonFSFixture(t)
	g := abandonFSGuard(t, exe, scope, u, digest, false, nil)
	f, _ := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
	if _, e := g.Finish(f); e != nil {
		t.Fatal(e)
	}
	g.Close()
	if _, e := ownedConfigBegin(t, exe, scope, u.RunID, u.TaskIDs, u.RequestSHA256); !errors.Is(e, ErrConfigPreparationAbandoned) {
		t.Fatal("valid terminal ignored by Begin", e)
	}
	if e := PinConfigPreparation(exe, u, digest); !errors.Is(e, ErrConfigPreparationAbandoned) {
		t.Fatal("valid terminal ignored by Pin", e)
	}
	if e := CheckConfigPreparationNotAbandoned(exe, scope, u.RunID); !errors.Is(e, ErrConfigPreparationAbandoned) {
		t.Fatal("valid terminal ignored by CPP lookup", e)
	}
	if _, e := ownedConfigBegin(t, exe, scope, "another-run", []string{"another-task"}, u.RequestSHA256); e != nil {
		t.Fatal("terminal blocked another run", e)
	}
}

func TestOwnedConfigAbandonHoldIdentityBeforeRepair(t *testing.T) {
	for _, kind := range []string{"run", "scope", "destination", "expected"} {
		t.Run(kind, func(t *testing.T) {
			dest, exe, scope, u, _ := abandonFSFixture(t)
			key := publicationBytesDigest([]byte(u.RunID))
			history := filepath.Join(dest, "owned-config", scope, "uses", key[:2], key+".json")
			if e := os.Remove(history); e != nil {
				t.Fatal(e)
			}
			wrong := u
			expected := ""
			switch kind {
			case "run":
				wrong.RunID = "foreign-run"
			case "scope":
				wrong.OwnerScopeSHA256 = strings.Repeat("f", 64)
			case "destination":
				wrong.Package.DestinationRoot = filepath.Join(dest, "foreign")
			case "expected":
				expected = strings.Repeat("f", 64)
			}
			raw, _ := json.Marshal(wrong)
			hold := filepath.Join(dest, "owned-config", scope, "preparations", key+".json")
			if e := os.WriteFile(hold, append(raw, '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			g, e := OpenConfigAbandonGuard(exe, scope, u.RunID, expected, nil)
			if g != nil {
				g.Close()
			}
			if e == nil {
				t.Error("foreign hold accepted")
			}
			polluted := publicationBytesDigest([]byte(wrong.RunID))
			path := filepath.Join(dest, "owned-config", scope, "uses", polluted[:2], polluted+".json")
			if _, e = os.Lstat(path); !os.IsNotExist(e) {
				t.Fatal("rejection polluted immutable history before identity validation", e)
			}
		})
	}
}

func TestOwnedConfigAbandonCompletedPendingIsNotShortcut(t *testing.T) {
	for _, kind := range []string{"terminal-pending-only", "terminal-competing-pending", "complete-competing-pending"} {
		t.Run(kind, func(t *testing.T) {
			dest, exe, scope, u, digest := abandonFSFixture(t)
			if e := PinConfigPreparation(exe, u, digest); e != nil {
				t.Fatal(e)
			}
			g := abandonFSGuard(t, exe, scope, u, digest, true, nil)
			f, _ := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
			if _, e := g.Finish(f); e != nil {
				t.Fatal(e)
			}
			g.Close()
			key := publicationBytesDigest([]byte(u.RunID))
			dir := filepath.Join(dest, "owned-config", scope, "abandoned", key[:2])
			name := key + ".terminal.json"
			if kind == "complete-competing-pending" {
				name = key + ".complete.json"
			}
			path := filepath.Join(dir, name)
			body, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "terminal-pending-only" {
				if e = os.Rename(path, filepath.Join(dir, ".pending-"+name)); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = os.WriteFile(filepath.Join(dir, ".pending-"+name), body, 0600); e != nil {
					t.Fatal(e)
				}
			}
			next, e := OpenConfigAbandonGuard(exe, scope, u.RunID, "", &f)
			if next != nil {
				defer next.Close()
			}
			if e == nil {
				t.Fatal("completed shortcut accepted a non-final or competing terminal/complete")
			}
		})
	}
}

func TestOwnedConfigAbandonFinalReferenceBarrier(t *testing.T) {
	for _, point := range []string{"before-complete", "complete"} {
		for _, kind := range []string{"pins-parent", "original-history"} {
			t.Run(point+"/"+kind, func(t *testing.T) {
				dest, exe, scope, u, digest := abandonFSFixture(t)
				if e := PinConfigPreparation(exe, u, digest); e != nil {
					t.Fatal(e)
				}
				originalPin, e := os.ReadFile(filepath.Join(dest, "pins", "task-a.json"))
				if e != nil {
					t.Fatal(e)
				}
				g := abandonFSGuard(t, exe, scope, u, digest, true, nil)
				defer g.Close()
				f, _ := NewConfigAbandonFence(u, "2026-10-07T00:00:00Z")
				reached := false
				_, e = g.finish(f, func(current string) error {
					if current != point {
						return nil
					}
					reached = true
					if kind == "pins-parent" {
						if x := os.Rename(filepath.Join(dest, "pins"), filepath.Join(dest, "original-pins")); x != nil {
							t.Fatal(x)
						}
						if x := os.Mkdir(filepath.Join(dest, "pins"), 0700); x != nil {
							t.Fatal(x)
						}
						if x := os.WriteFile(filepath.Join(dest, "pins", "task-a.json"), originalPin, 0600); x != nil {
							t.Fatal(x)
						}
					} else {
						key := publicationBytesDigest([]byte(u.RunID))
						replaceAbandonFile(t, filepath.Join(dest, "owned-config", scope, "uses", key[:2], key+".json"))
					}
					return nil
				})
				if !reached {
					t.Fatal("original final checkpoint was not reached", e)
				}
				if e == nil {
					t.Error("completion accepted changed reference proof after original absence check", point, kind)
				}
				if kind == "pins-parent" {
					if _, x := os.Lstat(filepath.Join(dest, "pins", "task-a.json")); x != nil {
						t.Fatal("foreign current reference was removed", x)
					}
				}
			})
		}
	}
}
