//go:build !windows

package main

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
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"golang.org/x/sys/unix"
)

// This lease orders this candidate's public writers; it grants no execution or
// recovery authority. The DB and filesystem remain separate commit domains.
type hostLaunchLease struct {
	paths          []string
	dirs           []*os.File // state, host-recovery, host-bootstrap
	lock           *os.File
	name           string
	bootstrapInfo  os.FileInfo
	bootstrapBytes []byte
}

func launchPrivateFile(info os.FileInfo) bool {
	return info != nil && configPreparationFileTrusted(info)
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
func (l *hostLaunchLease) close() {
	if l.lock != nil {
		_ = l.lock.Close()
	}
	for i := len(l.dirs) - 1; i >= 0; i-- {
		_ = l.dirs[i].Close()
	}
}
func (l *hostLaunchLease) validate() error {
	if len(l.paths) == 0 {
		return codeError("host_launch_lease_changed")
	}
	canonical, err := filepath.EvalSymlinks(l.paths[0])
	if err != nil || canonical != l.paths[0] {
		return codeError("host_launch_lease_changed")
	}
	for i, dir := range l.dirs {
		held, e := dir.Stat()
		named, n := os.Lstat(l.paths[i])
		if e != nil || n != nil || !launchDirectory(held, i == 2) || !launchDirectory(named, i == 2) || !os.SameFile(held, named) {
			return codeError("host_launch_lease_changed")
		}
	}
	if l.lock != nil {
		held, e := l.lock.Stat()
		named, n := launchOpenAt(l.dirs[1], l.lock.Name(), unix.O_RDONLY, 0)
		if n != nil {
			return codeError("host_launch_lease_changed")
		}
		current, n := named.Stat()
		_ = named.Close()
		if e != nil || n != nil || !launchPrivateFile(held) || !launchPrivateFile(current) || held.Size() != 0 || current.Size() != 0 || !os.SameFile(held, current) {
			return codeError("host_launch_lease_changed")
		}
	}
	return nil
}
func openHostLaunchLease(ctx context.Context, state string, c controlCapability) (_ *hostLaunchLease, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	id := c.HostLaunchID
	if c.Version != 2 || id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || c.HostLaunchToken == "" || c.RunID == "" || !filepath.IsAbs(state) || filepath.Clean(state) != state || c.BootstrapPath != filepath.Join(state, "host-bootstrap", id+".json") {
		return nil, codeError("host_launch_binding_mismatch")
	}
	fd, err := unix.Open(state, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	l := &hostLaunchLease{paths: []string{state}, dirs: []*os.File{os.NewFile(uintptr(fd), state)}, name: id + ".json"}
	defer func() {
		if err != nil {
			l.close()
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
func (l *hostLaunchLease) readBytes() ([]byte, os.FileInfo, error) {
	if err := l.validate(); err != nil {
		return nil, nil, err
	}
	f, err := launchOpenAt(l.dirs[2], l.name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !launchPrivateFile(before) || before.Size() < 1 || before.Size() > maxBootstrapBytes {
		return nil, nil, codeError("untrusted_host_bootstrap")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBootstrapBytes+1))
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	named, e := os.Lstat(filepath.Join(l.paths[2], l.name))
	if err != nil || e != nil || !launchPrivateFile(after) || !launchPrivateFile(named) || !os.SameFile(before, after) || !os.SameFile(after, named) || before.Size() != after.Size() || after.Size() != int64(len(raw)) || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(named.ModTime()) || len(raw) > maxBootstrapBytes {
		return nil, nil, codeError("host_bootstrap_changed")
	}
	return raw, after, l.validate()
}
func (l *hostLaunchLease) read(c controlCapability) (hostBootstrap, error) {
	var m hostBootstrapMetadataV2
	raw, info, err := l.readBytes()
	if err != nil {
		return hostBootstrap{}, err
	}
	if decode(raw, &m) != nil || m.Version != 2 || m.Hello.LaunchID != c.HostLaunchID || m.ProducerID != c.HostLaunchID || m.SocketPath != filepath.Join(l.paths[0], "coordinator.sock") || m.SpoolRoot != filepath.Join(l.paths[0], "host-spool", c.HostLaunchID) {
		return hostBootstrap{}, codeError("host_launch_binding_mismatch")
	}
	l.bootstrapBytes, l.bootstrapInfo = raw, info
	h := m.Hello
	b := hostBootstrap{Version: 2, SocketPath: m.SocketPath, SpoolRoot: m.SpoolRoot, ProducerID: m.ProducerID}
	b.Hello.LaunchID, b.Hello.LaunchToken = c.HostLaunchID, c.HostLaunchToken
	b.Hello.OriginContextID, b.Hello.OriginPID, b.Hello.OriginBirth = h.OriginContextID, h.OriginPID, h.OriginBirth
	b.Hello.HostGeneration, b.Hello.PID, b.Hello.Birth, b.Hello.Executable = h.HostGeneration, h.PID, h.Birth, h.Executable
	return b, nil
}
func (l *hostLaunchLease) unchanged() error {
	raw, info, err := l.readBytes()
	if err != nil {
		return err
	}
	if l.bootstrapInfo == nil || !os.SameFile(info, l.bootstrapInfo) || !bytes.Equal(raw, l.bootstrapBytes) {
		return codeError("host_bootstrap_changed")
	}
	return nil
}
func (l *hostLaunchLease) publish(b hostBootstrap) error {
	if err := l.unchanged(); err != nil {
		return err
	}
	raw, err := json.Marshal(bootstrapMetadata(b))
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxBootstrapBytes {
		return codeError("invalid_host_bootstrap")
	}
	suffix, err := randomValue("replace")
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
	if err = l.unchanged(); err != nil {
		return err
	}
	stage, err := launchOpenAt(dir, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	stageInfo, err := stage.Stat()
	stage.Close()
	if err != nil || !launchPrivateFile(stageInfo) || !os.SameFile(created, stageInfo) {
		return codeError("host_bootstrap_changed")
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
		return codeError("host_bootstrap_changed")
	}
	return nil
}
func (l *hostLaunchLease) writeRecoveryIntent(value any) error {
	if err := l.unchanged(); err != nil {
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
	return l.unchanged()
}
func rebindHostBootstrap(ctx context.Context, state string, c controlCapability, req coordinator.RebindOwnerRequest) (ipc.Envelope, error) {
	l, err := openHostLaunchLease(ctx, state, c)
	if err != nil {
		return ipc.Envelope{}, err
	}
	defer l.close()
	b, err := l.read(c)
	if err != nil {
		return ipc.Envelope{}, err
	}
	if req.OwnerMode == "local" {
		req.OriginContextID = b.Hello.OriginContextID
	}
	if req.OriginContextID != b.Hello.OriginContextID {
		return ipc.Envelope{}, codeError("host_launch_binding_mismatch")
	}
	if err = l.unchanged(); err != nil {
		return ipc.Envelope{}, err
	}
	rpc, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := call(rpc, state, ipc.KindRebindOwner, req)
	if err != nil {
		return ipc.Envelope{}, err
	}
	var receipt struct {
		Version         int    `json:"version"`
		Status          string `json:"status"`
		RunID           string `json:"run_id"`
		OriginContextID string `json:"origin_context_id"`
		HostGeneration  string `json:"host_generation"`
	}
	if decode(response.Payload, &receipt) != nil || receipt.Version != 1 || receipt.Status != "owner_rebound" || receipt.RunID != c.RunID || receipt.OriginContextID != req.OriginContextID || receipt.HostGeneration != req.HostGeneration {
		return ipc.Envelope{}, codeError("invalid_rebind_response")
	}
	if err = ctx.Err(); err != nil {
		return ipc.Envelope{}, err
	}
	b.Hello.OriginPID, b.Hello.OriginBirth, b.Hello.OriginContextID, b.Hello.HostGeneration = req.OriginPID, req.OriginBirth, req.OriginContextID, req.HostGeneration
	if err = l.publish(b); err != nil {
		return ipc.Envelope{}, err
	}
	return response, nil
}
