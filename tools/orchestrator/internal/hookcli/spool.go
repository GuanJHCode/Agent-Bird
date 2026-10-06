package hookcli

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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const (
	spoolMaxRecords     = 1000
	spoolMaxBytes       = 8 * 1024 * 1024
	spoolMaxRecordBytes = 64 * 1024
)

type spoolRecord struct {
	Event contract.BridgeHookEventV1
	Bytes []byte
	Hash  string
	Path  string
}

type spool struct {
	dir      string
	lockPath string
}

func spoolCode(code string) error { return errors.New(code) }

func ownedPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return spoolCode("bridge_spool_untrusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return spoolCode("bridge_spool_untrusted")
	}
	return nil
}

func openSpool(ctx context.Context, state, bindingID string) (*spool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || bindingID == "" || strings.ContainsAny(bindingID, `/\\`) {
		return nil, spoolCode("bridge_spool_untrusted")
	}
	if canonical, err := filepath.EvalSymlinks(state); err != nil || canonical != state {
		return nil, spoolCode("bridge_spool_untrusted")
	}
	if err := ownedPrivateDir(state); err != nil {
		return nil, err
	}
	root := filepath.Join(state, "bridge-spool")
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, spoolCode("bridge_spool_unavailable")
	}
	if err := ownedPrivateDir(root); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(bindingID))
	dir := filepath.Join(root, hex.EncodeToString(sum[:]))
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, spoolCode("bridge_spool_unavailable")
	}
	if err := ownedPrivateDir(dir); err != nil {
		return nil, err
	}
	return &spool{dir: dir, lockPath: filepath.Join(dir, ".lock")}, nil
}

func (s *spool) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(s.lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return spoolCode("bridge_spool_untrusted")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return spoolCode("bridge_spool_untrusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return spoolCode("bridge_spool_untrusted")
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return spoolCode("bridge_spool_unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	pathInfo, err := os.Lstat(s.lockPath)
	if err != nil || !os.SameFile(info, pathInfo) || pathInfo.Mode().Perm() != 0600 {
		return spoolCode("bridge_spool_untrusted")
	}
	if err := ownedPrivateDir(s.dir); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (s *spool) recordPath(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

func (s *spool) listLocked() ([]spoolRecord, int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, spoolCode("bridge_spool_unavailable")
	}
	records := make([]spoolRecord, 0, len(entries))
	total := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" {
			continue
		}
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") && len(name) == 69 {
			if _, err := hex.DecodeString(name[1:65]); err != nil {
				return nil, 0, spoolCode("bridge_spool_untrusted")
			}
			path := filepath.Join(s.dir, name)
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return nil, 0, spoolCode("bridge_spool_untrusted")
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
				return nil, 0, spoolCode("bridge_spool_untrusted")
			}
			if err := os.Remove(path); err != nil {
				return nil, 0, spoolCode("bridge_spool_unavailable")
			}
			if err := syncDir(s.dir); err != nil {
				return nil, 0, err
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") || len(name) != 69 {
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		path := filepath.Join(s.dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > spoolMaxRecordBytes {
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			f.Close()
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		raw := make([]byte, info.Size())
		_, err = io.ReadFull(f, raw)
		f.Close()
		if err != nil {
			return nil, 0, spoolCode("bridge_spool_unavailable")
		}
		event, err := contract.DecodeBridgeHookEventV1(raw)
		if err != nil || s.recordPath(event.EventID) != path {
			return nil, 0, spoolCode("bridge_spool_untrusted")
		}
		hash := sha256.Sum256(raw)
		records = append(records, spoolRecord{Event: event, Bytes: raw, Hash: hex.EncodeToString(hash[:]), Path: path})
		total += len(raw)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, total, nil
}

func (s *spool) pending(ctx context.Context) ([]spoolRecord, error) {
	var records []spoolRecord
	err := s.withLock(ctx, func() error { var err error; records, _, err = s.listLocked(); return err })
	return records, err
}

func (s *spool) put(ctx context.Context, event contract.BridgeHookEventV1) (spoolRecord, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return spoolRecord{}, spoolCode("bridge_invalid_input")
	}
	if len(raw) > spoolMaxRecordBytes {
		return spoolRecord{}, spoolCode("bridge_input_too_large")
	}
	if _, err := contract.DecodeBridgeHookEventV1(raw); err != nil {
		return spoolRecord{}, spoolCode("bridge_invalid_input")
	}
	path := s.recordPath(event.EventID)
	hash := sha256.Sum256(raw)
	record := spoolRecord{Event: event, Bytes: raw, Hash: hex.EncodeToString(hash[:]), Path: path}
	err = s.withLock(ctx, func() error {
		records, total, err := s.listLocked()
		if err != nil {
			return err
		}
		for _, old := range records {
			if old.Path != path {
				continue
			}
			if bytes.Equal(old.Bytes, raw) {
				return nil
			}
			return spoolCode("bridge_spool_conflict")
		}
		if len(records) >= spoolMaxRecords || total+len(raw) > spoolMaxBytes {
			return spoolCode("bridge_spool_full")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		tmp := filepath.Join(s.dir, "."+hex.EncodeToString(hash[:])+".tmp")
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return spoolCode("bridge_spool_unavailable")
		}
		defer os.Remove(tmp)
		if n, writeErr := f.Write(raw); writeErr != nil || n != len(raw) {
			f.Close()
			return spoolCode("bridge_spool_unavailable")
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return spoolCode("bridge_spool_unavailable")
		}
		if err := f.Close(); err != nil {
			return spoolCode("bridge_spool_unavailable")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return spoolCode("bridge_spool_unavailable")
		}
		return syncDir(s.dir)
	})
	return record, err
}

func (s *spool) ack(ctx context.Context, record spoolRecord, ack contract.BridgeIngressAck) error {
	if !ack.Committed || ack.EventID != record.Event.EventID || ack.PayloadHash != record.Hash {
		return spoolCode("bridge_ack_mismatch")
	}
	return s.withLock(ctx, func() error {
		records, _, err := s.listLocked()
		if err != nil {
			return err
		}
		for _, existing := range records {
			if existing.Path != record.Path {
				continue
			}
			if !bytes.Equal(existing.Bytes, record.Bytes) {
				return spoolCode("bridge_spool_conflict")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.Remove(existing.Path); err != nil {
				return spoolCode("bridge_spool_unavailable")
			}
			return syncDir(s.dir)
		}
		return spoolCode("bridge_spool_missing")
	})
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return spoolCode("bridge_spool_unavailable")
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return spoolCode("bridge_spool_unavailable")
	}
	return nil
}
