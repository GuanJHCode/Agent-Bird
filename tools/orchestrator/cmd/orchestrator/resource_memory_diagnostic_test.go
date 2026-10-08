package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Only a task-private test binary uses this entrypoint. It exposes no debug
// listener and neither forces GC nor changes production runtime configuration.
func runResourceMemoryDiagnosticFixture() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finish, err := startResourceMemorySamples(cancel)
	if err == nil {
		err = run(ctx, os.Args[1:], os.Stdout, os.Stderr)
		err = errors.Join(err, finish())
	}
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"version": 1, "status": "error", "error": err.Error()})
		os.Exit(2)
	}
}

func startResourceMemorySamples(cancel context.CancelFunc) (func() error, error) {
	if len(os.Args) < 2 {
		return nil, errors.New("diagnostic_role_missing")
	}
	role := os.Args[1]
	if role != "serve" && role != "source-host" && role != "original-owner" {
		return func() error { return nil }, nil
	}
	dir := os.Getenv("RESOURCE_MEMORY_TRACE_DIR")
	canonical, err := filepath.EvalSymlinks(dir)
	info, statErr := os.Lstat(dir)
	if err != nil || statErr != nil || !filepath.IsAbs(dir) || canonical != dir || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("diagnostic_directory_invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("diagnostic_directory_not_owned")
	}
	file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%s-%d.jsonl", role, os.Getpid())), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(file)
	sample := func() error {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if err := encoder.Encode(map[string]any{
			"unix_nano": time.Now().UnixNano(), "pid": os.Getpid(), "role": role,
			"heap_alloc": m.HeapAlloc, "heap_inuse": m.HeapInuse, "heap_idle": m.HeapIdle,
			"heap_released": m.HeapReleased, "stack_inuse": m.StackInuse, "sys": m.Sys,
			"total_alloc": m.TotalAlloc, "mallocs": m.Mallocs, "frees": m.Frees,
			"num_gc": m.NumGC, "next_gc": m.NextGC, "pause_total_ns": m.PauseTotalNs,
		}); err != nil {
			return err
		}
		return file.Sync()
	}
	if err = sample(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := sample(); err != nil {
					cancel()
					done <- errors.Join(err, file.Close())
					return
				}
			case <-stop:
				done <- errors.Join(sample(), file.Close())
				return
			}
		}
	}()
	return func() error { close(stop); return <-done }, nil
}
