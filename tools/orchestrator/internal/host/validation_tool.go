package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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
func nativeValidationToolFile(file *os.File, size int64) bool {
	return process.NativeExecutableFile(file, size)
}

func (h *Host) sealValidationTool(ctx context.Context, g contract.LaunchCommand, tool, pin, workspace, scratch string) (string, error) {
	invalid := errors.New("validation_tool_scope_invalid")
	if contract.RequireValidationTool(g, pin) != nil || h.producerID != g.HostID {
		return "", contract.ErrValidationToolGrant
	}
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
	if err != nil || !before.Mode().IsRegular() || before.Mode()&0111 == 0 || before.Size() < 4 || before.Size() > g.ValidationTool.MaximumBytes {
		return "", invalid
	}
	if !nativeValidationToolFile(file, before.Size()) {
		return "", errors.New("validation_tool_native_required")
	}
	original, err := process.ExecutableDigest(ctx, tool)
	if err != nil || original != pin {
		return "", errors.New("validation_tool_pin_changed")
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
		journal, e := readValidationToolJournal(dir, g)
		if e != nil || journal.Phase != "sealed" || journal.ContentSHA256 != pin {
			return "", errors.New("validation_tool_journal_invalid")
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
	directory, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	dev, ino, err := validationToolIdentity(directory)
	if err != nil {
		return "", err
	}
	journal := validationToolJournal{Version: 1, Phase: "intent", ReservationID: g.ValidationTool.ReservationID, ScopeSHA256: g.ValidationTool.ScopeSHA256, ToolSHA256: pin, DirectoryDev: dev, DirectoryIno: ino}
	if err = writeValidationToolJournal(dir, journal); err != nil {
		return "", err
	}
	if err = validationSyncDir(root); err != nil {
		return "", err
	}
	out, err := os.OpenFile(sealed, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0500)
	if err != nil {
		return "", err
	}
	outputInfo, err := out.Stat()
	if err != nil {
		out.Close()
		return "", err
	}
	journal.FileDev, journal.FileIno, err = validationToolIdentity(outputInfo)
	if err != nil {
		out.Close()
		return "", err
	}
	journal.Phase = "creating"
	if err = writeValidationToolJournal(dir, journal); err != nil {
		out.Close()
		return "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		out.Close()
		return "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	journal.Size, err = io.CopyBuffer(io.MultiWriter(out, hash), io.LimitReader(file, g.ValidationTool.MaximumBytes), buffer)
	if err == nil {
		err = out.Sync()
	}
	err = errors.Join(err, out.Close())
	after, e := file.Stat()
	if e != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		err = errors.Join(err, invalid)
	}
	journal.ContentSHA256 = hex.EncodeToString(hash.Sum(nil))
	journal.Phase = "sealed"
	if err != nil || journal.Size != before.Size() || journal.Size > g.ValidationTool.MaximumBytes || journal.ContentSHA256 != pin {
		journal.Phase = "partial"
		copied, e := os.Lstat(sealed)
		if e != nil {
			return "", errors.Join(err, e)
		}
		journal.Size = copied.Size()
		journal.ContentSHA256, e = process.ExecutableDigest(context.Background(), sealed)
		if e != nil {
			return "", errors.Join(err, e)
		}
		if e = writeValidationToolJournal(dir, journal); e != nil {
			return "", errors.Join(err, e)
		}
		return "", errors.Join(err, errors.New("validation_tool_pin_changed"))
	}
	if err = writeValidationToolJournal(dir, journal); err != nil {
		return "", err
	}
	if err = os.Chmod(dir, 0500); err != nil {
		return "", err
	}
	if err = validationSyncDir(dir); err != nil {
		return "", err
	}
	if err = validationSyncDir(root); err != nil {
		return "", err
	}
	actual, err := validationSealedDigest(ctx, sealed)
	if err != nil || actual != pin {
		return "", errors.New("validation_tool_pin_changed")
	}
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
