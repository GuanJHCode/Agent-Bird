package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const (
	ErrConflict  CodeError = "event_conflict"
	ErrUntrusted CodeError = "untrusted_spool"
)

type Spool struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Spool, error) {
	if !filepath.IsAbs(dir) {
		return nil, CodeError("path_not_absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !owned || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrUntrusted
	}
	return &Spool{dir: dir}, nil
}
func (s *Spool) Append(e contract.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Version != 1 || e.Sequence < 1 || e.AttemptID == "" || e.SegmentID == "" {
		return CodeError("invalid_event")
	}
	data, _ := json.Marshal(e)
	data = append(data, '\n')
	name := filepath.Join(s.dir, fmt.Sprintf("event-%020d.json", e.Sequence))
	if old, readErr := os.ReadFile(name); readErr == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		return ErrConflict
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	archived := filepath.Join(s.dir, "archive", filepath.Base(name))
	if old, err := os.ReadFile(archived); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		return ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := filepath.Join(s.dir, fmt.Sprintf(".event-%020d-%d.tmp", e.Sequence, os.Getpid()))
	fd, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if _, err = fd.Write(data); err == nil {
		err = fd.Sync()
	}
	cerr := fd.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// Link publishes the fully synced inode without replacing an existing
	// sequence. Readers never observe a partial JSON event.
	if err = os.Link(stage, name); errors.Is(err, os.ErrExist) {
		old, readErr := os.ReadFile(name)
		if readErr == nil && bytes.Equal(old, data) {
			_ = os.Remove(stage)
			return nil
		}
		return ErrConflict
	}
	if err != nil {
		return err
	}
	_ = os.Remove(stage)
	return syncDir(s.dir)
}

// Read retains full history for reconciliation, including archived records.
func (s *Spool) Read() ([]contract.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readEvents(0, true)
}

// ReadAfter reads only the unarchived suffix used by the publisher. Recovery
// that needs acknowledged history must use Read instead.
func (s *Spool) ReadAfter(after int64) ([]contract.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readEvents(after, false)
}
func (s *Spool) readEvents(after int64, history bool) ([]contract.Event, error) {
	paths, err := filepath.Glob(filepath.Join(s.dir, "event-*.json"))
	if err != nil {
		return nil, err
	}
	if history {
		archive, err := filepath.Glob(filepath.Join(s.dir, "archive", "event-*.json"))
		if err != nil {
			return nil, err
		}
		paths = append(paths, archive...)
	}
	out := make([]contract.Event, 0, len(paths))
	seen := map[int64][]byte{}
	for _, p := range paths {
		sequence, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "event-"), ".json"), 10, 64)
		if err != nil || sequence < 1 {
			return nil, CodeError("invalid_event")
		}
		if sequence <= after {
			continue
		}
		file, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 64*1024 {
			file.Close()
			return nil, CodeError("event_too_large")
		}
		data := make([]byte, info.Size())
		_, err = file.ReadAt(data, 0)
		file.Close()
		if err != nil {
			return nil, err
		}
		if old, exists := seen[sequence]; exists {
			if !bytes.Equal(old, data) {
				return nil, ErrConflict
			}
			continue
		}
		seen[sequence] = data
		var e contract.Event
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&e); err != nil || e.Sequence != sequence {
			return nil, CodeError("invalid_event")
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}
func (s *Spool) archiveThrough(sequence int64) error {
	dir := filepath.Join(s.dir, "archive")
	if _, err := Open(dir); err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(s.dir, "event-*.json"))
	if err != nil {
		return err
	}
	archived := []string{}
	for _, p := range paths {
		seq, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "event-"), ".json"), 10, 64)
		if err != nil {
			return CodeError("invalid_event")
		}
		if seq > sequence {
			continue
		}
		target := filepath.Join(dir, filepath.Base(p))
		if err = os.Link(p, target); errors.Is(err, os.ErrExist) {
			old, readErr := os.ReadFile(target)
			current, currentErr := os.ReadFile(p)
			if readErr != nil || currentErr != nil || !bytes.Equal(old, current) {
				return ErrConflict
			}
		} else if err != nil {
			return err
		}
		archived = append(archived, p)
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	for _, p := range archived {
		if err = os.Remove(p); err != nil {
			return err
		}
	}
	return syncDir(s.dir)
}
func (s *Spool) AckThrough(seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < 0 {
		return CodeError("invalid_ack")
	}
	current, err := s.readAck()
	if err != nil {
		return err
	}
	if seq < current {
		return CodeError("stale_ack")
	}
	data := []byte(fmt.Sprintf("{\"version\":1,\"acked_through\":%d}\n", seq))
	stage := filepath.Join(s.dir, ".ack.json.tmp")
	_ = os.Remove(stage)
	fd, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = fd.Write(data); err == nil {
		err = fd.Sync()
	}
	if cerr := fd.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(stage, filepath.Join(s.dir, "ack.json")); err != nil {
		return err
	}
	if err = syncDir(s.dir); err != nil {
		return err
	}
	return s.archiveThrough(seq)
}
func (s *Spool) AckedThrough() (int64, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.readAck() }
func (s *Spool) readAck() (int64, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "ack.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var v struct {
		Version int   `json:"version"`
		Acked   int64 `json:"acked_through"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&v); err != nil || v.Version != 1 || v.Acked < 0 {
		return 0, CodeError("invalid_ack")
	}
	return v.Acked, nil
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Spool) WriteArtifact(data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "result-output.bin")
	fd, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		old, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(old, data) {
			return path, nil
		}
		return "", ErrConflict
	}
	if err != nil {
		return "", err
	}
	if len(data) > 1024*1024 {
		_ = fd.Close()
		_ = os.Remove(path)
		return "", CodeError("artifact_too_large")
	}
	if _, err = fd.Write(data); err == nil {
		err = fd.Sync()
	}
	if closeErr := fd.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = syncDir(s.dir); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Spool) WriteMetadata(name string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || filepath.Base(name) != name || filepath.Ext(name) != ".json" {
		return CodeError("metadata_name_invalid")
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > 64*1024 {
		return CodeError("metadata_invalid")
	}
	data = append(data, '\n')
	stage := filepath.Join(s.dir, "."+name+".tmp")
	_ = os.Remove(stage)
	fd, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = fd.Write(data); err == nil {
		err = fd.Sync()
	}
	if closeErr := fd.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(stage)
		return err
	}
	if err = os.Rename(stage, filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(stage)
		return err
	}
	return syncDir(s.dir)
}
func (s *Spool) ReadMetadata(name string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || filepath.Base(name) != name {
		return CodeError("metadata_name_invalid")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	if len(data) > 64*1024 {
		return CodeError("metadata_too_large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(value); err != nil {
		return CodeError("metadata_invalid")
	}
	return nil
}
