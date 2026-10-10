package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBindFilteredTargetCandidateRejectsDetachedOwners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if pair, err := BindFilteredTargetCandidate(ctx, nil, nil); pair != nil || !IsCode(err, CodeReviewMismatch) {
		t.Fatalf("detached owners pair=%v err=%v", pair, err)
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
