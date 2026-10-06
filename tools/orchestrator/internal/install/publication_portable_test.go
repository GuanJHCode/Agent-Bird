package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func publicationPortableFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	files := map[string]string{}
	for rel, body := range map[string]string{"bin/codex-orchestrator": "synthetic executable", ".codex-plugin/plugin.json": `{"name":"codex-orchestrator","version":"1.0.0"}`, "runtime/g0/runtime-manifest.json": `{"version":2,"kind":"collect-only"}`} {
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		digest, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[rel] = digest
	}
	body, _ := json.Marshal(portableManifest{Version: "1.0.0", Files: files})
	if err := os.WriteFile(filepath.Join(source, "portable.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return source, filepath.Join(root, "installed"), filepath.Join(root, "state")
}

func TestInstallPublicationPortableConcurrentNormalizedRoots(t *testing.T) {
	source, destination, state := publicationPortableFixture(t)
	entered, release := make(chan string, 1), make(chan struct{})
	type outcome struct {
		path string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		path, err := installPortable(source, destination, state, func(point, normalized string) error {
			entered <- normalized
			<-release
			return nil
		})
		done <- outcome{path, err}
	}()
	loserRoot := <-entered
	winnerRoot := ""
	winner, winnerErr := installPortable(source, destination, state, func(point, normalized string) error {
		winnerRoot = normalized
		if _, err := os.Lstat(filepath.Join(destination, "versions", "1.0.0")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("both_installers_did_not_observe_absent")
		}
		return nil
	})
	close(release)
	loser := <-done
	if winnerErr != nil {
		t.Fatal(winnerErr)
	}
	if loserRoot == winnerRoot || loserRoot == "" || winnerRoot == "" {
		t.Fatal("independent canonical normalized source roots not proven")
	}
	if loser.err != nil || loser.path != winner {
		t.Fatalf("loser must revalidate the committed winner, not replay a different request: %v %q %q", loser.err, loser.path, winner)
	}
	entries, _ := os.ReadDir(filepath.Join(destination, "versions"))
	if len(entries) != 1 || entries[0].Name() != "1.0.0" {
		t.Fatal("duplicate or abandoned committed publication", entries)
	}
}

func TestInstallPublicationPortableRejectsUncommittedWinner(t *testing.T) {
	source, destination, state := publicationPortableFixture(t)
	interrupted := errors.New("interrupted_winner")
	_, err := installPortable(source, destination, state, func(point, normalized string) error {
		competitor := t.TempDir()
		if err := copyTree(normalized, competitor); err != nil {
			return err
		}
		options := InstallOptions{SourceRoot: competitor, BinaryPath: filepath.Join(competitor, "bin", "codex-orchestrator"), DestinationRoot: destination, DataRoot: state, Version: "1.0.0"}
		_, err := installPackage(options, func(point string) error {
			if point == "version-published" {
				return interrupted
			}
			return nil
		})
		if !errors.Is(err, interrupted) {
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("uncommitted version was accepted as winner")
	}
	intentPath := filepath.Join(destination, "install-intents", "1.0.0.json")
	before, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal("real interrupted original evidence missing", err)
	}
	if _, err = InstallPortable(source, destination, state); err == nil {
		t.Fatal("pending visible version accepted")
	}
	after, _ := os.ReadFile(intentPath)
	if !bytes.Equal(before, after) {
		t.Fatal("pending winner adopted or repaired using a different normalized source")
	}
}

func TestInstallPublicationPortableDifferentBytesAreRejected(t *testing.T) {
	source, destination, state := publicationPortableFixture(t)
	_, err := installPortable(source, destination, state, func(point, normalized string) error {
		// The competitor commits a coherent package with different binary bytes.
		competitor := t.TempDir()
		if err := copyTree(normalized, competitor); err != nil {
			return err
		}
		binary := filepath.Join(competitor, "bin", "codex-orchestrator")
		if err := os.WriteFile(binary, []byte("different coherent winner"), 0700); err != nil {
			return err
		}
		_, err := InstallPackage(InstallOptions{SourceRoot: competitor, BinaryPath: binary, DestinationRoot: destination, DataRoot: state, Version: "1.0.0"})
		return err
	})
	if err == nil {
		t.Fatal("different winner bytes accepted")
	}
	actual, err := os.ReadFile(filepath.Join(destination, "versions", "1.0.0", "bin", "codex-orchestrator"))
	if err != nil || string(actual) != "different coherent winner" {
		t.Fatal("different committed package overwritten", err)
	}
}
