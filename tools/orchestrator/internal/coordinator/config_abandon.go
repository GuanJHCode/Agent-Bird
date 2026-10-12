//go:build !windows

package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

// Owner is trusted localOwner output, independently reauthenticated by Store.
// This shared core is also called by pure real-FS/SQLite regressions; those do
// not claim a real kernel peer or physical process/UDS admission observation.
func AbandonConfigPreparation(ctx context.Context, db *store.DB, owner store.OwnerGrant, exe, state, run, expected string) (install.ConfigAbandonReceipt, error) {
	var receipt install.ConfigAbandonReceipt
	scope, e := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if e != nil {
		return receipt, e
	}
	original, e := db.ConfigAbandonFence(ctx, owner, run)
	if e != nil {
		return receipt, e
	}
	guard, e := install.OpenConfigAbandonGuard(exe, scope, run, expected, original)
	if e != nil {
		return receipt, e
	}
	defer guard.Close()
	if !guard.Completed() {
		// Authenticate the actual original Store before any history repair or new FS publication.
		use := guard.Use()
		if _, e = db.ConfigPreparationAuthority(ctx, owner, use.Authority); e != nil {
			return receipt, e
		}
		if e = guard.InspectCleanup(func(u install.ConfigPreparationUse) (string, bool, error) {
			return readAbandonCPP(state, scope, run, u)
		}); e != nil {
			return receipt, e
		}
	}
	fence, e := db.CommitConfigAbandon(ctx, owner, guard)
	if e != nil {
		return receipt, e
	}
	return guard.Finish(fence)
}

// Read through original directory/file FDs without taking the caller's CPP
// lock. Exact EXCL-created CPP is immutable; no mkdir/materialize occurs here.
func readAbandonCPP(state, scope, run string, u install.ConfigPreparationUse) (string, bool, error) {
	if u.Authority == nil || state != u.Authority.StateRoot || !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return "", false, install.ErrConfigPreparationStoreMismatch
	}
	var dirs []*os.File
	var paths []string
	defer func() {
		for i := len(dirs) - 1; i >= 0; i-- {
			dirs[i].Close()
		}
	}()
	fd, e := unix.Open(state, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return "", false, e
	}
	dirs = append(dirs, os.NewFile(uintptr(fd), state))
	paths = append(paths, state)
	validate := func() error {
		for i, f := range dirs {
			opened, e := f.Stat()
			current, x := os.Lstat(paths[i])
			if e != nil || x != nil || !current.IsDir() || current.Mode().Perm() != 0700 || !os.SameFile(opened, current) {
				return install.ErrConfigAbandonConflict
			}
			st, ok := current.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != uint32(os.Geteuid()) {
				return install.ErrConfigAbandonConflict
			}
		}
		return nil
	}
	for _, part := range []string{"owners", "config-preparations", scope} {
		if e = validate(); e != nil {
			return "", false, e
		}
		fd, e = unix.Openat(int(dirs[len(dirs)-1].Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, os.ErrNotExist) {
			return "", false, validate()
		}
		if e != nil {
			return "", false, e
		}
		path := filepath.Join(paths[len(paths)-1], part)
		dirs = append(dirs, os.NewFile(uintptr(fd), path))
		paths = append(paths, path)
	}
	if e = validate(); e != nil {
		return "", false, e
	}
	sum := configRunKey(run)
	name := sum + ".json"
	parent := dirs[len(dirs)-1]
	fd, e = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(e, os.ErrNotExist) {
		return "", false, validate()
	}
	if e != nil {
		return "", false, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	trusted := func(info os.FileInfo) bool {
		st, ok := info.Sys().(*syscall.Stat_t)
		return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && info.Size() > 0 && info.Size() <= 128<<10
	}
	before, e := f.Stat()
	if e != nil || !trusted(before) {
		return "", true, install.ErrConfigAbandonConflict
	}
	raw, e := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if e != nil {
		return "", true, e
	}
	after, e := f.Stat()
	current, x := os.Lstat(filepath.Join(paths[len(paths)-1], name))
	if e != nil || x != nil || !trusted(after) || !trusted(current) || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || after.Size() != current.Size() || after.ModTime() != current.ModTime() || len(raw) > 128<<10 {
		return "", true, install.ErrConfigAbandonConflict
	}
	p, decodeErr := DecodeConfigPreparedPlan(raw)
	if decodeErr != nil {
		return "", true, install.ErrConfigAbandonConflict
	}
	canonical, e := json.Marshal(p)
	if e != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return "", true, install.ErrConfigAbandonConflict
	}
	if e = ValidateConfigPreparedStructure(p, u); e != nil {
		return "", true, e
	}
	return p.ResolvedPlanSHA256, true, validate()
}

func configRunKey(run string) string {
	sum := sha256.Sum256([]byte(run))
	return hex.EncodeToString(sum[:])
}
