package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Header-only synthetic bytes test the opaque file seal, not SQL semantics.
// These fixtures never start an actor or establish original Wait/tree proof.
func agySQLiteHeaderFixture() []byte {
	body := make([]byte, 128)
	copy(body, "SQLite format 3\x00")
	body[16], body[17], body[18], body[19] = 0x80, 0, 2, 2
	return body
}

type agySQLiteSealFixture struct {
	h                   *Host
	first, resume       contract.LaunchCommand
	conversations, path string
	prepareResume       func() error
}

func agyRetainedSealFixture(t *testing.T, kind string) agySQLiteSealFixture {
	t.Helper()
	h, cmd, profile, first, original := agyHistoryFixture(t)
	prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), first)
	if err != nil {
		t.Fatal(err)
	}
	resume := agyHistoryCompletedSourceFixture(t, h, first)
	conversations := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, prepared), "conversations")
	if err = os.Mkdir(conversations, 0700); err != nil {
		t.Fatal(err)
	}
	ext, body := ".pb", []byte("native-protobuf-fixture")
	if kind == "sqlite-v1" {
		ext, body = ".db", agySQLiteHeaderFixture()
	}
	path := filepath.Join(conversations, resume.SessionID+ext)
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return agySQLiteSealFixture{h: h, first: first, resume: resume, conversations: conversations, path: path,
		prepareResume: func() error {
			_, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "second"), resume)
			return err
		}}
}

func (f agySQLiteSealFixture) capture() error {
	return f.h.captureAGYHistorySeal(f.first, &process.Identity{PID: 123, PGID: 123, Birth: "pure-stored-identity"}, contract.EventQuestion)
}

func agySealOccupant(t *testing.T, path, kind string) {
	t.Helper()
	var err error
	switch kind {
	case "empty":
		err = os.WriteFile(path, nil, 0600)
	case "content":
		err = os.WriteFile(path, []byte("occupant"), 0600)
	case "directory":
		err = os.Mkdir(path, 0700)
	case "dangling_link":
		err = os.Symlink(path+".missing", path)
	case "fifo":
		err = syscall.Mkfifo(path, 0600)
	default:
		t.Fatal("unknown fixture occupant")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestAGYSQLiteSealRejectsSidecarsAndMixedBackends(t *testing.T) {
	for _, kind := range []string{"sqlite-v1", "protobuf-v1"} {
		for _, phase := range []string{"capture", "resume"} {
			for _, suffix := range []string{"-wal", "-shm", "-journal", "alternate"} {
				for _, occupant := range []string{"empty", "content", "directory", "dangling_link", "fifo"} {
					t.Run(kind+"/"+phase+"/"+suffix+"/"+occupant, func(t *testing.T) {
						f := agyRetainedSealFixture(t, kind)
						if phase == "resume" {
							if err := f.capture(); err != nil {
								t.Fatal(err)
							}
						}
						path := filepath.Join(f.conversations, f.resume.SessionID+".db"+suffix)
						if suffix == "alternate" {
							ext := ".db"
							if kind == "sqlite-v1" {
								ext = ".pb"
							}
							path = filepath.Join(f.conversations, f.resume.SessionID+ext)
						}
						agySealOccupant(t, path, occupant)
						var err error
						if phase == "resume" {
							err = f.prepareResume()
						} else {
							err = f.capture()
						}
						if err == nil {
							t.Fatal("sidecar or mixed-backend occupant admitted")
						}
					})
				}
			}
		}
	}
}

func TestAGYSQLiteSealVersionKindAndExactPathGuards(t *testing.T) {
	for _, row := range []struct {
		name, initial, kind, pathSuffix string
		version                         int
		valid                           bool
	}{
		{"v2_sqlite", "sqlite-v1", "sqlite-v1", ".db", 2, true},
		{"v2_protobuf", "protobuf-v1", "protobuf-v1", ".pb", 2, true},
		{"v1_protobuf", "protobuf-v1", "", ".pb", 1, true},
		{"v1_reinterpreted", "protobuf-v1", "protobuf-v1", ".pb", 1, false},
		{"v1_sqlite_kind", "sqlite-v1", "sqlite-v1", ".db", 1, false},
		{"v1_sqlite_empty", "sqlite-v1", "", ".db", 1, false},
		{"unknown_version", "sqlite-v1", "sqlite-v1", ".db", 3, false},
		{"unknown_kind", "sqlite-v1", "untrusted-v1", ".db", 2, false},
		{"missing_kind", "sqlite-v1", "", ".db", 2, false},
		{"kind_drift", "sqlite-v1", "protobuf-v1", ".db", 2, false},
		{"path_drift", "sqlite-v1", "sqlite-v1", ".pb", 2, false},
		{"foreign_path", "sqlite-v1", "sqlite-v1", ".foreign", 2, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := agyRetainedSealFixture(t, row.initial)
			if err := f.capture(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.h.spoolRoot, f.first.AttemptID, f.first.SegmentID, agySealName)
			var seal agyHistorySeal
			if err := readContinuationMetadata(path, &seal); err != nil {
				t.Fatal(err)
			}
			seal.Version, seal.StorageKind = row.version, row.kind
			seal.TrajectoryPath = filepath.Join(f.conversations, f.resume.SessionID+row.pathSuffix)
			body, err := json.Marshal(seal)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if row.name == "v1_protobuf" {
				for _, occupant := range []string{".db", ".db-wal", ".db-shm", ".db-journal"} {
					p := filepath.Join(f.conversations, f.resume.SessionID+occupant)
					if err = os.WriteFile(p, nil, 0600); err != nil {
						t.Fatal(err)
					}
					if err = f.prepareResume(); err == nil {
						t.Fatal("v1 protobuf compatibility admitted another backend or sidecar")
					}
					if err = os.Remove(p); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = f.prepareResume()
			if (err == nil) != row.valid {
				t.Fatalf("version/kind/exact path admission changed: valid=%t err=%v", row.valid, err)
			}
			current, err := os.ReadFile(path)
			if err != nil || string(current) != string(original) {
				t.Fatal("resume rewrote or repaired the source receipt")
			}
		})
	}
}

func TestAGYSQLiteSealNextCompletedSegmentBindsUpdatedDatabase(t *testing.T) {
	f := agyRetainedSealFixture(t, "sqlite-v1")
	if err := f.capture(); err != nil {
		t.Fatal(err)
	}
	firstReceipt := filepath.Join(f.h.spoolRoot, f.first.AttemptID, f.first.SegmentID, agySealName)
	original, err := os.ReadFile(firstReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.prepareResume(); err != nil {
		t.Fatal(err)
	}
	updated := agySQLiteHeaderFixture()
	updated[100] = 1
	if err = os.WriteFile(f.path+".native.tmp", updated, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.path+".native.tmp", f.path); err != nil {
		t.Fatal(err)
	}
	// Match the accepted multi-segment fixture: retain the original session
	// event and bind distinct current-segment events/process facts. No OS Wait.
	spool, err := f.h.spool(store.Attempt{ID: f.resume.AttemptID, SegmentID: f.resume.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	processID := &contract.ProcessIdentity{PID: 124, PGID: 124, Birth: "pure-second-identity"}
	exit := 0
	for i, kind := range []string{contract.EventSession, contract.EventQuestion, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: f.h.producerID, EventID: kind + "-second", Kind: kind,
			RunID: f.first.RunID, TaskID: f.first.TaskID, AttemptID: f.first.AttemptID, SegmentID: f.resume.SegmentID,
			WorkRevision: 1, Sequence: int64(i + 4), PayloadHash: hashText(kind + "-second"), Process: processID}
		if kind == contract.EventExited {
			e.ExitCode = &exit
		} else {
			e.SessionKind, e.SessionID = f.resume.SessionKind, f.resume.SessionID
		}
		if kind == contract.EventQuestion {
			e.QuestionID, e.QuestionRevision, e.QuestionKind = "question-second", 2, "technical"
		}
		if err = spool.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	third := f.resume
	copyRef := *f.resume.Continuation
	third.Continuation = &copyRef
	third.SegmentID, third.QuestionID, third.QuestionRevision = "third", "question-second", 2
	third.Continuation.SourceSegmentID, third.Continuation.SourceEventID = f.resume.SegmentID, contract.EventQuestion+"-second"
	third.Continuation.SourceEventRevision, third.Continuation.SourceEventSHA256 = 5, hashText(contract.EventQuestion+"-second")
	if err = f.h.captureAGYHistorySeal(f.resume, &process.Identity{PID: 124, PGID: 124, Birth: "pure-second-identity"}, contract.EventQuestion); err != nil {
		t.Fatal(err)
	}
	var history agyHistoryBinding
	if err = readContinuationMetadata(filepath.Join(f.h.spoolRoot, f.first.AttemptID, f.first.SegmentID, "provider-history.json"), &history); err != nil {
		t.Fatal(err)
	}
	if err = f.h.validateAGYHistorySeal(third, history); err != nil {
		t.Fatalf("new completed source incorrectly tied to prior DB inode/bytes: %v", err)
	}
	if err = f.h.validateAGYHistorySeal(f.resume, history); err == nil {
		t.Fatal("previous source receipt admitted the updated database")
	}
	current, err := os.ReadFile(firstReceipt)
	if err != nil || string(current) != string(original) {
		t.Fatal("next-segment seal rewrote the original receipt")
	}
}

func TestAGYSQLiteSealRejectsUnsafeOrChangedFiles(t *testing.T) {
	for _, phase := range []string{"capture", "resume"} {
		for _, fault := range []string{"missing", "invalid_header", "truncated_header", "oversized", "hardlink", "symlink", "permissions", "replace_same_bytes", "inplace_same_length"} {
			t.Run(phase+"/"+fault, func(t *testing.T) {
				f := agyRetainedSealFixture(t, "sqlite-v1")
				if phase == "resume" {
					if err := f.capture(); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch fault {
				case "missing":
					err = os.Remove(f.path)
				case "invalid_header":
					err = os.WriteFile(f.path, []byte(strings.Repeat("x", 128)), 0600)
				case "truncated_header":
					err = os.WriteFile(f.path, []byte("SQLite format 3\x00"), 0600)
				case "oversized":
					err = os.Truncate(f.path, 16*1024*1024+1)
				case "hardlink":
					err = os.Link(f.path, f.path+".alias")
				case "symlink":
					err = os.Rename(f.path, f.path+".saved")
					if err == nil {
						err = os.Symlink(f.path+".saved", f.path)
					}
				case "permissions":
					err = os.Chmod(f.path, 0666)
				case "replace_same_bytes":
					err = os.WriteFile(f.path+".new", agySQLiteHeaderFixture(), 0600)
					if err == nil {
						err = os.Rename(f.path+".new", f.path)
					}
				case "inplace_same_length":
					body := agySQLiteHeaderFixture()
					body[100] = 1
					err = os.WriteFile(f.path, body, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if phase == "resume" {
					err = f.prepareResume()
				} else {
					err = f.capture()
				}
				validCurrentFile := phase == "capture" && (fault == "replace_same_bytes" || fault == "inplace_same_length")
				if validCurrentFile && err != nil {
					t.Fatalf("capture must bind current complete file, not a previous segment inode: %v", err)
				} else if !validCurrentFile && err == nil {
					t.Fatal("missing, unsafe or changed retained database admitted")
				}
			})
		}
	}
}

func TestAGYSQLiteSealChecksPostReadState(t *testing.T) {
	for _, kind := range []string{"sqlite-v1", "protobuf-v1"} {
		for _, change := range []string{"none", "alternate", "wal", "shm", "journal", "hardlink", "replace_same_bytes", "inplace_same_length"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f := agyRetainedSealFixture(t, kind)
				readerCalled := false
				reader := func(path, name string, limit int64, sqlite bool) (agyConfigReference, error) {
					readerCalled = true
					file, err := agyFileSnapshotChecked(path, name, limit, sqlite)
					if err != nil {
						return file, err
					}
					if change == "alternate" {
						ext := ".db"
						if kind == "sqlite-v1" {
							ext = ".pb"
						}
						err = os.WriteFile(filepath.Join(f.conversations, f.resume.SessionID+ext), nil, 0600)
					} else if change == "wal" || change == "shm" || change == "journal" {
						err = os.WriteFile(filepath.Join(f.conversations, f.resume.SessionID+".db-"+change), nil, 0600)
					} else if change == "hardlink" {
						err = os.Link(path, path+".alias")
					} else if change == "replace_same_bytes" || change == "inplace_same_length" {
						body, readErr := os.ReadFile(path)
						if readErr != nil {
							t.Fatal(readErr)
						}
						if change == "replace_same_bytes" {
							err = os.WriteFile(path+".new", body, 0600)
							if err == nil {
								err = os.Rename(path+".new", path)
							}
						} else {
							body[len(body)-1] ^= 1
							err = os.WriteFile(path, body, 0600)
						}
					}
					return file, err
				}
				_, _, err := agyTrajectorySnapshot(f.conversations, f.resume.SessionID, kind, reader)
				if !readerCalled {
					t.Fatal("causal real-file read did not complete")
				}
				if change == "none" && err != nil {
					t.Fatal(err)
				} else if change != "none" && err == nil {
					t.Fatal("file state changed after real read but before capture completion was admitted")
				}
			})
		}
	}
}

func TestAGYSQLiteSealAcceptsUnambiguousRetainedDatabase(t *testing.T) {
	h, cmd, profile, first, original := agyHistoryFixture(t)
	prepared, err := h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "scratch"), first)
	if err != nil {
		t.Fatal(err)
	}
	resume := agyHistoryCompletedSourceFixture(t, h, first)
	conversations := filepath.Join(filepath.Dir(original), agyHistoryArgument(t, prepared), "conversations")
	if err = os.Mkdir(conversations, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(conversations, resume.SessionID+".db")
	if err = os.WriteFile(path, agySQLiteHeaderFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err = h.captureAGYHistorySeal(first, &process.Identity{PID: 123, PGID: 123, Birth: "pure-stored-identity"}, contract.EventQuestion); err != nil {
		t.Fatalf("original completed source with only its SQLite history refused: %v", err)
	}
	sealPath := filepath.Join(h.spoolRoot, first.AttemptID, first.SegmentID, agySealName)
	body, err := os.ReadFile(sealPath)
	if err != nil {
		t.Fatal(err)
	}
	var seal map[string]any
	if json.Unmarshal(body, &seal) != nil || seal["version"] != float64(2) || seal["storage_kind"] != "sqlite-v1" || seal["trajectory_path"] != path || seal["sha256"] != hashBytes(agySQLiteHeaderFixture()) {
		t.Fatal("SQLite receipt did not bind its explicit format, path and original bytes")
	}
	if _, err = h.sandboxProfileCommand(context.Background(), cmd, profile, filepath.Join(h.spoolRoot, "second"), resume); err != nil {
		t.Fatalf("exact authenticated SQLite source refused before Start: %v", err)
	}
}
