//go:build !windows

package hostbootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func replacementSuffix() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "replace-" + hex.EncodeToString(value[:]), nil
}

// This lease orders this candidate's public writers; it grants no execution or
// recovery authority. The DB and filesystem remain separate commit domains.
type Lease struct {
	paths          []string
	dirs           []*os.File // state, host-recovery, host-bootstrap
	lock           *os.File
	name           string
	launchID       string
	bootstrapInfo  os.FileInfo
	bootstrapBytes []byte
}

func launchPrivateFile(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
func launchDirectory(info os.FileInfo, bootstrap bool) bool {
	if info == nil || !info.IsDir() || !owned(info) {
		return false
	}
	// A read-only private bootstrap directory is still safe to inspect. A later
	// publication will fail, leaving the authenticated replay able to repair it.
	return info.Mode().Perm() == 0700 || (bootstrap && info.Mode().Perm() == 0500)
}
func launchOpenAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (l *Lease) Close() {
	if l.lock != nil {
		_ = l.lock.Close()
	}
	for i := len(l.dirs) - 1; i >= 0; i-- {
		_ = l.dirs[i].Close()
	}
}
func (l *Lease) validate() error {
	if len(l.paths) == 0 {
		return Error("host_launch_lease_changed")
	}
	canonical, err := filepath.EvalSymlinks(l.paths[0])
	if err != nil || canonical != l.paths[0] {
		return Error("host_launch_lease_changed")
	}
	for i, dir := range l.dirs {
		held, e := dir.Stat()
		named, n := os.Lstat(l.paths[i])
		if e != nil || n != nil || !launchDirectory(held, i == 2) || !launchDirectory(named, i == 2) || !os.SameFile(held, named) {
			return Error("host_launch_lease_changed")
		}
	}
	if l.lock != nil {
		held, e := l.lock.Stat()
		named, n := launchOpenAt(l.dirs[1], l.lock.Name(), unix.O_RDONLY, 0)
		if n != nil {
			return Error("host_launch_lease_changed")
		}
		current, n := named.Stat()
		_ = named.Close()
		if e != nil || n != nil || !launchPrivateFile(held) || !launchPrivateFile(current) || held.Size() != 0 || current.Size() != 0 || !os.SameFile(held, current) {
			return Error("host_launch_lease_changed")
		}
	}
	return nil
}
func Open(ctx context.Context, state string, id string) (_ *Lease, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return nil, Error("host_launch_binding_mismatch")
	}
	fd, err := unix.Open(state, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	l := &Lease{paths: []string{state}, dirs: []*os.File{os.NewFile(uintptr(fd), state)}, name: id + ".json", launchID: id}
	defer func() {
		if err != nil {
			l.Close()
		}
	}()
	if err = l.validate(); err != nil {
		return nil, err
	}
	if err = unix.Mkdirat(fd, "host-recovery", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	if err = l.dirs[0].Sync(); err != nil {
		return nil, err
	}
	for _, part := range []string{"host-recovery", "host-bootstrap"} {
		dir, e := launchOpenAt(l.dirs[0], part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if e != nil {
			return nil, e
		}
		l.paths = append(l.paths, filepath.Join(state, part))
		l.dirs = append(l.dirs, dir)
	}
	if err = l.validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(id))
	l.lock, err = launchOpenAt(l.dirs[1], hex.EncodeToString(sum[:])+".lock", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	if err = l.validate(); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err = wait.Err(); err != nil {
			return nil, err
		}
		err = unix.Flock(int(l.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			return nil, wait.Err()
		case <-timer.C:
		}
	}
	if err = l.validate(); err != nil {
		return nil, err
	}
	if err = l.lock.Sync(); err != nil {
		return nil, err
	}
	if err = l.dirs[1].Sync(); err != nil {
		return nil, err
	}
	return l, nil
}
func (l *Lease) readBytes() ([]byte, os.FileInfo, error) {
	if err := l.validate(); err != nil {
		return nil, nil, err
	}
	f, err := launchOpenAt(l.dirs[2], l.name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !launchPrivateFile(before) || before.Size() < 1 || before.Size() > MaxBytes {
		return nil, nil, Error("untrusted_host_bootstrap")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	named, e := os.Lstat(filepath.Join(l.paths[2], l.name))
	if err != nil || e != nil || !launchPrivateFile(after) || !launchPrivateFile(named) || !os.SameFile(before, after) || !os.SameFile(after, named) || before.Size() != after.Size() || after.Size() != int64(len(raw)) || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(named.ModTime()) || len(raw) > MaxBytes {
		return nil, nil, Error("host_bootstrap_changed")
	}
	return raw, after, l.validate()
}
func decodeMetadata(raw []byte, target *MetadataV2) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Error("invalid_json")
	}
	return nil
}

func (l *Lease) Read() (MetadataV2, error) {
	var m MetadataV2
	raw, info, err := l.readBytes()
	if err != nil {
		return m, err
	}
	if decodeMetadata(raw, &m) != nil || m.Version != 2 || (m.OwnerLaunchProtocol != 0 && m.OwnerLaunchProtocol != 2) || m.Hello.LaunchID != l.launchID || m.ProducerID != l.launchID || m.SocketPath != filepath.Join(l.paths[0], "coordinator.sock") || m.SpoolRoot != filepath.Join(l.paths[0], "host-spool", l.launchID) {
		return MetadataV2{}, Error("host_launch_binding_mismatch")
	}
	l.bootstrapBytes, l.bootstrapInfo = raw, info
	return m, nil
}

func (l *Lease) Unchanged() error {
	raw, info, err := l.readBytes()
	if err != nil {
		return err
	}
	if l.bootstrapInfo == nil || !os.SameFile(info, l.bootstrapInfo) || !bytes.Equal(raw, l.bootstrapBytes) {
		return Error("host_bootstrap_changed")
	}
	return nil
}
func (l *Lease) Publish(metadata MetadataV2) error {
	if err := l.Unchanged(); err != nil {
		return err
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxBytes {
		return Error("invalid_host_bootstrap")
	}
	suffix, err := replacementSuffix()
	if err != nil {
		return err
	}
	name := "." + l.name + "." + suffix
	dir := l.dirs[2]
	f, err := launchOpenAt(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// Keep the created inode open until rename and postcheck; never remove an
	// externally substituted temporary file on an error path.
	defer f.Close()
	created, err := f.Stat()
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if published {
			return
		}
		current, e := launchOpenAt(dir, name, unix.O_RDONLY, 0)
		if e != nil {
			return
		}
		info, e := current.Stat()
		current.Close()
		if e == nil && os.SameFile(created, info) {
			_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		}
	}()
	n, err := f.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = l.Unchanged(); err != nil {
		return err
	}
	stage, err := launchOpenAt(dir, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	stageInfo, err := stage.Stat()
	stageBytes, readErr := io.ReadAll(io.LimitReader(stage, MaxBytes+1))
	stage.Close()
	if err != nil || readErr != nil || !launchPrivateFile(stageInfo) || !os.SameFile(created, stageInfo) || !bytes.Equal(stageBytes, raw) {
		return Error("host_bootstrap_changed")
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), l.name); err != nil {
		return err
	}
	published = true
	if err = dir.Sync(); err != nil {
		return err
	}
	body, info, err := l.readBytes()
	if err != nil {
		return err
	}
	if !os.SameFile(created, info) || !bytes.Equal(raw, body) {
		return Error("host_bootstrap_changed")
	}
	return nil
}

// PublishStable is the server-owned replay publisher.  Its stage name is
// derived from the frozen expected bytes, so a SIGKILL during a write leaves
// at most one resumable inode for the authenticated publication journal.
// Existing malformed stages are evidence of tampering and are deliberately
// retained for diagnosis instead of being removed by a later request.
func (l *Lease) PublishStable(metadata MetadataV2) error {
	if err := l.Unchanged(); err != nil {
		return err
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxBytes {
		return Error("invalid_host_bootstrap")
	}
	sum := sha256.Sum256(append([]byte(l.launchID+"\x00"), raw...))
	name := ".publication-" + hex.EncodeToString(sum[:])
	dir := l.dirs[2]
	created := false
	f, err := launchOpenAt(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		f, err = launchOpenAt(dir, name, unix.O_RDWR, 0)
		if err != nil {
			return err
		}
	} else {
		created = true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !launchPrivateFile(info) || info.Size() > int64(len(raw)) {
		return Error("host_bootstrap_changed")
	}
	current, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || !bytes.Equal(current, raw[:len(current)]) {
		return Error("host_bootstrap_changed")
	}
	published := false
	defer func() {
		if !created || published {
			return
		}
		named, e := launchOpenAt(dir, name, unix.O_RDONLY, 0)
		if e != nil {
			return
		}
		defer named.Close()
		now, e := named.Stat()
		if e == nil && os.SameFile(info, now) {
			_ = unix.Unlinkat(int(dir.Fd()), name, 0)
			_ = dir.Sync()
		}
	}()
	if len(current) < len(raw) {
		if _, err = f.Seek(int64(len(current)), io.SeekStart); err != nil {
			return err
		}
		n, e := f.Write(raw[len(current):])
		if e != nil {
			return e
		}
		if n != len(raw)-len(current) {
			return io.ErrShortWrite
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = l.Unchanged(); err != nil {
		return err
	}
	stage, err := launchOpenAt(dir, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	stageInfo, statErr := stage.Stat()
	staged, readErr := io.ReadAll(io.LimitReader(stage, MaxBytes+1))
	_ = stage.Close()
	if statErr != nil || readErr != nil || !launchPrivateFile(stageInfo) || !os.SameFile(info, stageInfo) || !bytes.Equal(staged, raw) {
		return Error("host_bootstrap_changed")
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), l.name); err != nil {
		return err
	}
	published = true
	if err = dir.Sync(); err != nil {
		return err
	}
	body, final, err := l.readBytes()
	if err != nil || !os.SameFile(info, final) || !bytes.Equal(body, raw) {
		return Error("host_bootstrap_changed")
	}
	return nil
}
func (l *Lease) WriteRecoveryIntent(value any) error {
	if err := l.Unchanged(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := launchOpenAt(l.dirs[1], l.lock.Name()+".intent.json", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = l.dirs[1].Sync(); err != nil {
		return err
	}
	return l.Unchanged()
}
