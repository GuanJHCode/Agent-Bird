package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func loadStartupLimits(state string, db *store.DB) error {
	f, err := os.OpenFile(filepath.Join(state, "runtime-limits.json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return store.CodeError("concurrency_limits_untrusted")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Geteuid()) || info.Size() > 16384 {
		return store.CodeError("concurrency_limits_untrusted")
	}
	raw := make([]byte, info.Size())
	if _, err = f.ReadAt(raw, 0); err != nil {
		return err
	}
	return db.ConfigureConcurrency(context.Background(), raw)
}
