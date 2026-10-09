package host

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validationPathWithin(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func nativeValidationTool(body []byte) bool {
	if len(body) < 4 || bytes.HasPrefix(body, []byte("#!")) {
		return false
	}
	reader := bytes.NewReader(body)
	magic := binary.BigEndian.Uint32(body[:4])
	switch magic {
	case 0x7f454c46:
		f, e := elf.NewFile(reader)
		return e == nil && (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN)
	case 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe:
		f, e := macho.NewFile(reader)
		return e == nil && f.Type == macho.TypeExec
	case 0xcafebabe, 0xbebafeca:
		f, e := macho.NewFatFile(reader)
		if e != nil || len(f.Arches) == 0 {
			return false
		}
		for _, a := range f.Arches {
			if a.Type != macho.TypeExec {
				return false
			}
		}
		return true
	}
	return false
}

func (h *Host) sealValidationTool(ctx context.Context, g contract.LaunchCommand, tool, pin, workspace, scratch string) (string, error) {
	invalid := errors.New("validation_tool_scope_invalid")
	root := filepath.Join(h.spoolRoot, ".validation-tools")
	if validationPathWithin(root, workspace) || validationPathWithin(root, scratch) {
		return "", invalid
	}
	canonical, err := filepath.EvalSymlinks(tool)
	if err != nil || canonical != tool || !filepath.IsAbs(tool) {
		return "", invalid
	}
	for _, name := range []string{"env", "sh", "bash", "dash", "zsh", "ksh", "csh", "tcsh", "fish", "busybox", "toybox"} {
		if filepath.Base(tool) == name {
			return "", errors.New("validation_tool_dispatcher_unsupported")
		}
	}
	file, err := os.OpenFile(tool, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode()&0111 == 0 || before.Size() < 4 || before.Size() > 128*1024*1024 {
		return "", invalid
	}
	body, err := io.ReadAll(io.LimitReader(file, 128*1024*1024+1))
	if err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || int64(len(body)) != before.Size() {
		return "", invalid
	}
	if hashBytes(body) != pin {
		return "", errors.New("validation_tool_pin_changed")
	}
	if !nativeValidationTool(body) {
		return "", errors.New("validation_tool_native_required")
	}
	// Reject known distributor binaries even when copied under another name.
	for _, path := range []string{"/usr/bin/env", "/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh", "/bin/csh", "/bin/tcsh"} {
		digest, e := process.ExecutableDigest(ctx, path)
		if e == nil && digest == pin {
			return "", errors.New("validation_tool_dispatcher_unsupported")
		}
	}
	if err = validationOwnedDir(root, 0700, true); err != nil {
		return "", err
	}
	sealed := h.validationSealedPath(g)
	dir := filepath.Dir(sealed)
	if _, err = os.Lstat(dir); err == nil {
		if err = validationOwnedDir(dir, 0500, false); err != nil {
			return "", err
		}
		actual, e := validationSealedDigest(ctx, sealed)
		if e != nil || actual != pin {
			return "", errors.New("validation_tool_pin_changed")
		}
		return sealed, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	out, err := os.OpenFile(sealed, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0500)
	if err != nil {
		return "", err
	}
	if _, err = out.Write(body); err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Chmod(dir, 0500); err != nil {
		return "", err
	}
	for _, path := range []string{dir, root} {
		f, e := os.Open(path)
		if e != nil {
			return "", e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return "", e
		}
	}
	actual, err := validationSealedDigest(ctx, sealed)
	if err != nil || actual != pin {
		return "", errors.New("validation_tool_pin_changed")
	}
	// Original requested identity must still match after copying; Start repeats it.
	actual, err = process.ExecutableDigest(ctx, tool)
	if err != nil || actual != pin {
		return "", errors.New("validation_tool_pin_changed")
	}
	return sealed, nil
}
func validationOwnedDir(path string, mode os.FileMode, create bool) error {
	if create {
		if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || st.Uid != uint32(os.Geteuid()) {
		return errors.New("validation_tool_scope_invalid")
	}
	return nil
}

func (h *Host) validationSealedPath(g contract.LaunchCommand) string {
	return filepath.Join(h.spoolRoot, ".validation-tools", hashText(g.RunID+"\x00"+g.TaskID+"\x00"+g.AttemptID+"\x00"+g.SegmentID+"\x00"+g.CommandID), "tool")
}

func validationSealedDigest(ctx context.Context, path string) (string, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return "", e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0500 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return "", errors.New("validation_tool_scope_invalid")
	}
	return process.ExecutableDigest(ctx, path)
}
