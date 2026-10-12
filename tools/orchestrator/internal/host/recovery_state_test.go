package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryAttemptsPersistAcrossAuthorizedPIDChange(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	state, err := openHostRecovery(dir, "stable-launch-scope", 10, "old-birth")
	if err != nil {
		t.Fatal(err)
	}
	when, err := state.reserve(time.UnixMilli(100_000))
	if err != nil || when.UnixMilli() != 101_000 {
		t.Fatalf("first retry=%v err=%v", when, err)
	}
	state.close()
	reopened, err := openHostRecovery(dir, "stable-launch-scope", 20, "new-birth")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if reopened.record.Attempts != 1 || reopened.record.NextAttemptUnixMS != 101_000 {
		t.Fatalf("PID change reset recovery: %+v", reopened.record)
	}
	when, err = reopened.reserve(time.UnixMilli(100_500))
	if err != nil || when.UnixMilli() != 101_000 || reopened.record.Attempts != 1 {
		t.Fatalf("restart skipped or recharged pending recovery: %v %+v %v", when, reopened.record, err)
	}
	if err := reopened.attemptStarted(); err != nil {
		t.Fatal(err)
	}
	when, err = reopened.reserve(time.UnixMilli(200_000))
	if err != nil || when.UnixMilli() != 202_000 || reopened.record.Attempts != 2 {
		t.Fatalf("second retry reset: when=%v record=%+v err=%v", when, reopened.record, err)
	}
}

func TestRecoveryReservationsAreFiniteAndNeverResetOnSuccess(t *testing.T) {
	state, err := openHostRecovery(privateRecoveryTestDirectory(t), "stable-launch-scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	for i, item := range []struct{ now, want int64 }{{100_000, 101_000}, {200_000, 202_000}, {300_000, 304_000}, {400_000, 408_000}, {500_000, 516_000}} {
		// Independent literal expected intervals 1/2/4/8/16 seconds.
		when, err := state.reserve(time.UnixMilli(item.now))
		if err != nil || when.UnixMilli() != item.want || state.record.Attempts != i+1 {
			t.Fatalf("retry %d when=%v record=%+v err=%v", i+1, when, state.record, err)
		}
		if err := state.attemptStarted(); err != nil {
			t.Fatal(err)
		}
		if err := state.authenticatedHost(10, "birth"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.reserve(time.UnixMilli(600_000)); !errors.Is(err, errCoordinatorUnavailable) {
		t.Fatalf("sixth recovery permitted: %v", err)
	}
	if !state.record.Unavailable {
		t.Fatal("exhaustion was not persisted")
	}
}

func TestRecoveryRecordRejectsForeignMalformedAndLinkedInputs(t *testing.T) {
	for _, kind := range []string{"scope", "malformed", "symlink", "hardlink", "public-mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			state, err := openHostRecovery(dir, "stable-launch-scope", 10, "birth")
			if err != nil {
				t.Fatal(err)
			}
			state.close()
			path := filepath.Join(dir, ".coordinator-recovery.json")
			wantedScope := "stable-launch-scope"
			switch kind {
			case "scope":
				wantedScope = "another-launch-scope"
			case "malformed":
				err = os.WriteFile(path, []byte(`{"version":1,"attempts":0,"unknown":true}`), 0600)
			case "symlink":
				err = os.Rename(path, path+".owned")
				if err == nil {
					err = os.Symlink(path+".owned", path)
				}
			case "hardlink":
				err = os.Link(path, path+".other")
			case "public-mode":
				err = os.Chmod(path, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if opened, err := openHostRecovery(dir, wantedScope, 10, "birth"); err == nil {
				opened.close()
				t.Fatalf("unsafe %s record accepted", kind)
			}
		})
	}
}

func TestRecoveryRecordRefusesConcurrentWriter(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	first, err := openHostRecovery(dir, "stable-launch-scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if second, err := openHostRecovery(dir, "stable-launch-scope", 10, "birth"); err == nil {
		second.close()
		t.Fatal("concurrent recovery writer acquired the same ledger")
	}
}

func privateRecoveryTestDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
