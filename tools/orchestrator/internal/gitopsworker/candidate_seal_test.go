package gitopsworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func sealSource(kind, spool, command string, binding gitops.CandidateBinding) gitops.CandidateResourceSource {
	key, prefix := command, "gitops-journal/"
	if kind == "producer" {
		key, prefix = binding.RunID+"\x00"+binding.TaskID+"\x00"+binding.AttemptID+"\x00"+binding.SegmentID, ".gitops-journal/"
	}
	sum := sha256.Sum256([]byte(key))
	return gitops.CandidateResourceSource{Kind: kind, HostID: "host", CommandID: command, Binding: binding, JournalRelative: prefix + hex.EncodeToString(sum[:]), ProjectionSHA256: strings.Repeat("a", 64)}
}

func sealedFixture(t *testing.T) (string, *durableJournal, gitops.CandidateResourceSource, gitops.CandidateReceipt) {
	t.Helper()
	spool := t.TempDir()
	if err := os.Chmod(spool, 0700); err != nil {
		t.Fatal(err)
	}
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
	source := sealSource("producer", spool, "command", binding)
	root := filepath.Join(spool, source.JournalRelative)
	j, err := openJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	id := gitops.DirectoryIdentity{Device: 1, Inode: 2, Mode: 040700, UID: int64(os.Geteuid())}
	receipt := gitops.CandidateReceipt{Binding: &binding, AttemptID: binding.AttemptID, RepoRoot: "/repo", Worktree: "/work", CommonDir: "/common", BaseOID: strings.Repeat("1", 40), CandidateOID: strings.Repeat("2", 40), TreeOID: strings.Repeat("3", 40), RootIdentity: id, WorktreeIdentity: "/work", RepoRootIdentity: id, CommonDirIdentity: id, Files: map[string]gitops.FileIdentity{"x": {Inode: 1}}, PlanRevision: 1, PrivateRef: sealedPrivateRef(source), PrivateRefExpectedOID: strings.Repeat("2", 40)}
	proc := &process.Identity{PID: 41, PGID: 41, Birth: "birth", BirthKnown: true, Executable: "/usr/bin/git", ExecutableSHA256: strings.Repeat("b", 64), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, p := range []gitops.GitProcessRecord{{Phase: "intent", CommandDigest: strings.Repeat("c", 64)}, {Phase: "spawned", CommandDigest: strings.Repeat("c", 64), Process: proc}, {Phase: "exited", CommandDigest: strings.Repeat("c", 64), Process: proc}} {
		if err = j.RecordGitProcess(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if err = j.RecordCandidateFreeze(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	source.ProjectionSHA256 = gitops.CandidateResourceReceiptDigest(receipt)
	return spool, j, source, receipt
}

func TestSealCandidateResourceExactReopenAndAppendFreeze(t *testing.T) {
	spool, j, source, want := sealedFixture(t)
	defer j.close()
	header, err := j.SealCandidateResource(context.Background(), spool, source)
	if err != nil {
		t.Fatal(err)
	}
	if header.ReceiptSHA256 != gitops.CandidateResourceReceiptDigest(want) || header.Source.ProjectionSHA256 != source.ProjectionSHA256 {
		t.Fatalf("header=%+v", header)
	}
	got, err := OpenSealedCandidateResource(context.Background(), spool, header)
	if err != nil || got.CandidateOID != want.CandidateOID {
		t.Fatalf("receipt=%+v err=%v", got, err)
	}
	if err = j.append("later", map[string]string{"x": "y"}); err == nil {
		t.Fatal("sealed journal accepted append")
	}
	if sealed, err := j.CandidateResourceSealed(context.Background()); err != nil || !sealed {
		t.Fatalf("sealed=%t err=%v", sealed, err)
	}
	if again, err := j.SealCandidateResource(context.Background(), spool, source); err != nil || again != header {
		t.Fatalf("repeat=%+v err=%v", again, err)
	}
	j.close()
	if again, err := SealCandidateJournal(context.Background(), spool, source); err != nil || again != header {
		t.Fatalf("external repeat=%+v err=%v", again, err)
	}
}

func TestCandidateSealComposeProjectionStripsFilesAndAddsBinding(t *testing.T) {
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
	source := sealSource("compose", t.TempDir(), "compose-command", binding)
	receipt := gitops.CandidateReceipt{Files: map[string]gitops.FileIdentity{"kept-in-full": {Inode: 1}}, PrivateRef: "refs/orchestrator/g3/compose-command", Binding: nil}
	projection, err := candidateProjection(source, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if projection == gitops.CandidateResourceReceiptDigest(receipt) {
		t.Fatal("compose projection retained full receipt digest")
	}
	public := receipt
	public.Files, public.Binding = nil, &binding
	if projection != gitops.CandidateResourceReceiptDigest(public) {
		t.Fatal("compose projection is not Host receipt digest")
	}
}

func TestSealCandidateResourceRejectsUnfinishedOrOversize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*durableJournal) error
	}{
		{"unfinished", func(j *durableJournal) error {
			return j.RecordGitProcess(context.Background(), gitops.GitProcessRecord{Phase: "intent", CommandDigest: strings.Repeat("d", 64)})
		}},
		{"oversize", func(j *durableJournal) error { return os.Truncate(j.file.Name(), journalLimit+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool, j, source, _ := sealedFixture(t)
			defer j.close()
			if err := tc.mutate(j); err != nil {
				t.Fatal(err)
			}
			if _, err := j.SealCandidateResource(context.Background(), spool, source); err == nil {
				t.Fatal("seal unexpectedly succeeded")
			}
		})
	}
}

func TestOpenSealedCandidateResourceRejectsReplacement(t *testing.T) {
	spool, j, source, _ := sealedFixture(t)
	header, err := j.SealCandidateResource(context.Background(), spool, source)
	if err != nil {
		t.Fatal(err)
	}
	j.close()
	dir := filepath.Join(spool, source.JournalRelative)
	if err = os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenSealedCandidateResource(context.Background(), spool, header); err == nil {
		t.Fatal("replacement accepted")
	}
}
