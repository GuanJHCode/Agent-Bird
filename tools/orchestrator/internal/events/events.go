package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
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
func (s *Spool) Read() ([]contract.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(s.dir, "event-*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]contract.Event, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if len(data) > 64*1024 {
			return nil, CodeError("event_too_large")
		}
		var e contract.Event
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err = d.Decode(&e); err != nil {
			return nil, CodeError("invalid_event")
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Spool) AckThrough(seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < 0 {
		return CodeError("invalid_ack")
	}
	current, _ := s.readAck()
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
	return syncDir(s.dir)
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
