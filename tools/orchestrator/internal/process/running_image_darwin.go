//go:build darwin

package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const runningImageLsofLimit = 1024 * 1024

// VerifyRunningExecutable proves that path is still the executable image of
// identity's live process, then hashes that same opened vnode. It deliberately
// does not inspect argv, environment, credentials, or loaded libraries.
func VerifyRunningExecutable(ctx context.Context, identity Identity, path, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if identity.PID <= 0 || !identity.BirthKnown || identity.Birth == "" || !filepath.IsAbs(path) || digest == "" {
		return CodeError("running_executable_invalid")
	}
	if !sameBirth(identity) {
		return CodeError("running_executable_identity_changed")
	}
	kernelPath, err := ExecutablePath(identity.PID)
	if err != nil || kernelPath != path {
		return CodeError("running_executable_path_mismatch")
	}
	dev, ino, err := runningImageVnode(ctx, identity.PID, kernelPath)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return CodeError("running_executable_open_failed")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !validRunningExecutable(info) {
		return CodeError("running_executable_invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != dev || uint64(st.Ino) != ino {
		return CodeError("running_executable_vnode_mismatch")
	}
	actual, err := digestRunningExecutable(ctx, f, info)
	if err != nil || !strings.EqualFold(actual, digest) {
		return CodeError("running_executable_digest_mismatch")
	}
	if !sameBirth(identity) {
		return CodeError("running_executable_identity_changed")
	}
	currentPath, err := ExecutablePath(identity.PID)
	if err != nil || currentPath != kernelPath {
		return CodeError("running_executable_identity_changed")
	}
	return nil
}

func sameBirth(identity Identity) bool {
	birth, err := Birth(identity.PID)
	return err == nil && birth == identity.Birth
}

func validRunningExecutable(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 && info.Size() > 0 && info.Size() <= 512*1024*1024
}

func digestRunningExecutable(ctx context.Context, f *os.File, before os.FileInfo) (string, error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		total += int64(n)
		if total > 512*1024*1024 {
			return "", CodeError("running_executable_invalid")
		}
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	after, err := f.Stat()
	if err != nil || total != before.Size() || after.Size() != before.Size() || after.ModTime() != before.ModTime() {
		return "", CodeError("running_executable_changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runningImageVnode(ctx context.Context, pid int, path string) (uint64, uint64, error) {
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-FfnDi")
	var output limitedBuffer
	output.limit = runningImageLsofLimit
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil || output.exceeded {
		return 0, 0, CodeError("running_executable_kernel_unknown")
	}
	for _, record := range parseLsofText(output.buf.String()) {
		if record.name != path {
			continue
		}
		dev, devErr := strconv.ParseUint(record.device, 0, 64)
		ino, inoErr := strconv.ParseUint(record.inode, 0, 64)
		if devErr == nil && inoErr == nil && dev != 0 && ino != 0 {
			return dev, ino, nil
		}
	}
	return 0, 0, CodeError("running_executable_kernel_unknown")
}

type lsofTextRecord struct{ name, device, inode string }

func parseLsofText(out string) []lsofTextRecord {
	var records []lsofTextRecord
	var current *lsofTextRecord
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'f':
			if current != nil {
				records = append(records, *current)
			}
			if line[1:] == "txt" {
				current = &lsofTextRecord{}
			} else {
				current = nil
			}
		case 'D':
			if current != nil {
				current.device = line[1:]
			}
		case 'i':
			if current != nil {
				current.inode = line[1:]
			}
		case 'n':
			if current != nil {
				current.name = line[1:]
			}
		}
	}
	if current != nil {
		records = append(records, *current)
	}
	return records
}

type limitedBuffer struct {
	buf      strings.Builder
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}
