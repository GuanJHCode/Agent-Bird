package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"

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
func nativeValidationToolFile(file *os.File, size int64) bool {
	var head [64]byte
	n, e := file.ReadAt(head[:], 0)
	if e != nil && e != io.EOF || n < 32 || bytes.HasPrefix(head[:n], []byte("#!")) {
		return false
	}
	magic := binary.BigEndian.Uint32(head[:4])
	if magic == 0x7f454c46 {
		if n < 64 || head[6] != 1 || head[4] != 2 || head[5] != 1 && head[5] != 2 {
			return false
		}
		var order binary.ByteOrder = binary.LittleEndian
		if head[5] == 2 {
			order = binary.BigEndian
		}
		typ := order.Uint16(head[16:18])
		entry := order.Uint64(head[24:32])
		off := order.Uint64(head[32:40])
		entrySize := order.Uint16(head[54:56])
		count := order.Uint16(head[56:58])
		if (typ != 2 && typ != 3) || entry == 0 || entrySize != 56 || count < 1 || count > 256 || off > uint64(size) || uint64(count)*56 > uint64(size)-off {
			return false
		}
		var ph [56]byte
		executable := false
		for i := uint16(0); i < count; i++ {
			if _, e = file.ReadAt(ph[:], int64(off)+int64(i)*56); e != nil {
				return false
			}
			if order.Uint32(ph[:4]) == 1 {
				at := order.Uint64(ph[8:16])
				vaddr := order.Uint64(ph[16:24])
				length := order.Uint64(ph[32:40])
				mem := order.Uint64(ph[40:48])
				if at > uint64(size) || length > uint64(size)-at || length > mem {
					return false
				}
				executable = executable || (order.Uint32(ph[4:8])&1 != 0 && entry >= vaddr && entry-vaddr < mem)
			}
		}
		return executable
	}
	if magic == 0xcafebabe || magic == 0xbebafeca {
		var order binary.ByteOrder = binary.BigEndian
		if magic == 0xbebafeca {
			order = binary.LittleEndian
		}
		count := order.Uint32(head[4:8])
		if count < 1 || count > 32 || int64(8+count*20) > size {
			return false
		}
		var arch [20]byte
		for i := uint32(0); i < count; i++ {
			if _, e = file.ReadAt(arch[:], int64(8+i*20)); e != nil {
				return false
			}
			at := order.Uint32(arch[8:12])
			length := order.Uint32(arch[12:16])
			if length < 32 || int64(at)+int64(length) > size || !nativeMachOHeader(file, int64(at), int64(length)) {
				return false
			}
		}
		return true
	}
	return nativeMachOHeader(file, 0, size)
}
func nativeMachOHeader(file *os.File, at, size int64) bool {
	var h [32]byte
	if _, e := file.ReadAt(h[:], at); e != nil {
		return false
	}
	magic := binary.BigEndian.Uint32(h[:4])
	var order binary.ByteOrder = binary.BigEndian
	headerSize := int64(28)
	switch magic {
	case 0xfeedface:
	case 0xcefaedfe:
		order = binary.LittleEndian
	case 0xfeedfacf:
		headerSize = 32
	case 0xcffaedfe:
		headerSize = 32
		order = binary.LittleEndian
	default:
		return false
	}
	count := order.Uint32(h[16:20])
	commands := order.Uint32(h[20:24])
	if order.Uint32(h[12:16]) != 2 || count < 1 || count > 256 || commands > 1<<20 || int64(commands) > size-headerSize {
		return false
	}
	offset := headerSize
	entry := false
	var command [8]byte
	for i := uint32(0); i < count; i++ {
		if _, e := file.ReadAt(command[:], at+offset); e != nil {
			return false
		}
		kind := order.Uint32(command[:4])
		length := order.Uint32(command[4:8])
		if length < 8 || int64(length) > headerSize+int64(commands)-offset {
			return false
		}
		entry = entry || kind == 0x80000028 || kind == 0x5
		offset += int64(length)
	}
	return entry && offset == headerSize+int64(commands)
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
