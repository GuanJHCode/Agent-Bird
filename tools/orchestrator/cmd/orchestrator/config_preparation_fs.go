//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type configPreparationFiles struct {
	paths       []string
	directories []*os.File
	lock        *os.File
}

func configPreparationFileTrusted(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}

func (f *configPreparationFiles) validate() error {
	for i, dir := range f.directories {
		opened, err := dir.Stat()
		if err != nil {
			return err
		}
		current, err := os.Lstat(f.paths[i])
		if err != nil || !current.IsDir() || current.Mode().Perm() != 0700 || !owned(current) || !os.SameFile(opened, current) {
			return codeError("config_preparation_parent_changed")
		}
	}
	if f.lock != nil {
		opened, err := f.lock.Stat()
		if err != nil {
			return err
		}
		current, err := os.Lstat(filepath.Join(f.paths[len(f.paths)-1], f.lock.Name()))
		if err != nil || !configPreparationFileTrusted(current) || current.Size() != 0 || !os.SameFile(opened, current) {
			return codeError("config_preparation_lock_changed")
		}
	}
	return nil
}

func (f *configPreparationFiles) close() {
	if f.lock != nil {
		_ = unix.Flock(int(f.lock.Fd()), unix.LOCK_UN)
		f.lock.Close()
	}
	for i := len(f.directories) - 1; i >= 0; i-- {
		f.directories[i].Close()
	}
}

func openConfigPreparationFiles(ctx context.Context, state, scope, run string) (*configPreparationFiles, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || !configDigest(scope) || run == "" {
		return nil, codeError("config_preparation_invalid")
	}
	f := &configPreparationFiles{}
	fd, err := unix.Open(state, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f.paths = append(f.paths, state)
	f.directories = append(f.directories, os.NewFile(uintptr(fd), state))
	for _, part := range []string{"owners", "config-preparations", scope} {
		parent := f.directories[len(f.directories)-1]
		if err = unix.Mkdirat(int(parent.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			f.close()
			return nil, err
		}
		if err = parent.Sync(); err != nil {
			f.close()
			return nil, err
		}
		fd, err = unix.Openat(int(parent.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			f.close()
			return nil, err
		}
		path := filepath.Join(f.paths[len(f.paths)-1], part)
		f.paths = append(f.paths, path)
		f.directories = append(f.directories, os.NewFile(uintptr(fd), path))
	}
	if err = f.validate(); err != nil {
		f.close()
		return nil, err
	}
	name := filepath.Base(configPreparationPath(state, scope, run)) + ".lock"
	parent := f.directories[len(f.directories)-1]
	fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		f.close()
		return nil, err
	}
	f.lock = os.NewFile(uintptr(fd), name)
	info, err := f.lock.Stat()
	if err != nil || !configPreparationFileTrusted(info) || info.Size() != 0 {
		f.close()
		return nil, codeError("config_preparation_lock_invalid")
	}
	// This controls only competing preparation I/O; it grants no worker time.
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			f.close()
			return nil, codeError("config_preparation_busy")
		case <-timer.C:
		}
	}
	if err = f.validate(); err != nil {
		f.close()
		return nil, err
	}
	return f, nil
}

func (f *configPreparationFiles) read(name string) (configPreparedPlan, bool, error) {
	var p configPreparedPlan
	if err := f.validate(); err != nil {
		return p, false, err
	}
	dir := f.directories[len(f.directories)-1]
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !configPreparationFileTrusted(before) || before.Size() < 1 || before.Size() > configPreparationLimit {
		return p, true, codeError("config_preparation_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, configPreparationLimit+1))
	if err != nil {
		return p, true, err
	}
	after, err := file.Stat()
	current, e := os.Lstat(filepath.Join(f.paths[len(f.paths)-1], name))
	if err != nil || e != nil || !configPreparationFileTrusted(after) || !configPreparationFileTrusted(current) || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || after.Size() != current.Size() || !after.ModTime().Equal(current.ModTime()) || !before.ModTime().Equal(after.ModTime()) || len(raw) > configPreparationLimit {
		return p, true, codeError("config_preparation_changed")
	}
	if err = decode(raw, &p); err != nil {
		return p, true, err
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return p, true, codeError("config_preparation_invalid")
	}
	return p, true, f.validate()
}

func (f *configPreparationFiles) write(name string, p configPreparedPlan) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > configPreparationLimit {
		return codeError("config_preparation_capacity")
	}
	if err = f.validate(); err != nil {
		return err
	}
	dir := f.directories[len(f.directories)-1]
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	n, err := file.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	written, statErr := file.Stat()
	if err == nil {
		err = statErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	} // Preserve partial record and original install hold.
	if err = dir.Sync(); err != nil {
		return err
	}
	identity, e := os.Lstat(filepath.Join(f.paths[len(f.paths)-1], name))
	if e != nil || !os.SameFile(written, identity) {
		return codeError("config_preparation_changed")
	}
	current, exists, err := f.read(name)
	if err != nil || !exists || configPreparedDigest(current) != configPreparedDigest(p) {
		return codeError("config_preparation_changed")
	}
	return f.validate()
}
