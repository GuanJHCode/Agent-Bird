package gitops

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBindFilteredTargetCandidateRejectsDetachedOwners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if pair, err := BindFilteredTargetCandidate(ctx, nil, nil); pair != nil || !IsCode(err, CodeReviewMismatch) {
		t.Fatalf("detached owners pair=%v err=%v", pair, err)
	}
}

func TestPairReadIndexRejectsNamedReplacement(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index")
	raw := make([]byte, 32)
	copy(raw, "DIRC")
	binary.BigEndian.PutUint32(raw[4:8], 2)
	if err := os.WriteFile(indexPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	gitDir, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer gitDir.Close()
	index, err := filterOpenAt(gitDir, "index", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, _, err = pairReadIndex(index, gitDir); err != nil {
		t.Fatalf("read original index: %v", err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err = os.WriteFile(replacement, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, indexPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err = pairReadIndex(index, gitDir); err == nil {
		t.Fatal("held index accepted named replacement")
	}
}

func TestFilterTargetCandidatePairCloseOnlyClosesPairIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, []byte("pair"), 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pair := &FilterTargetCandidatePair{target: &FilterTargetObservation{}, candidate: &FilterMaterialization{}, childIndex: fd}
	if err = pair.Close(); err != nil || pair.childIndex != nil || pair.target == nil || pair.candidate == nil {
		t.Fatalf("pair close err=%v pair=%#v", err, pair)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("pair close changed borrowed file: %v", err)
	}
}
