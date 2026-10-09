//go:build darwin

package gitfilter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
)

type node struct {
	Device, Inode, Nlink uint64
	UID                  uint32
	Mode                 uint32
	Size, Modified       int64
}

func identity(f *os.File) (node, error) {
	s, e := f.Stat()
	if e != nil {
		return node{}, e
	}
	raw, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return node{}, errors.New("filter_identity_invalid")
	}
	return node{uint64(raw.Dev), raw.Ino, uint64(raw.Nlink), raw.Uid, uint32(s.Mode()), s.Size(), s.ModTime().UnixNano()}, nil
}
func named(path string) (node, error) {
	s, e := os.Lstat(path)
	if e != nil {
		return node{}, e
	}
	raw, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return node{}, errors.New("filter_identity_invalid")
	}
	return node{uint64(raw.Dev), raw.Ino, uint64(raw.Nlink), raw.Uid, uint32(s.Mode()), s.Size(), s.ModTime().UnixNano()}, nil
}
func digestValid(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func hashFD(ctx context.Context, f *os.File, n int64) (string, error) {
	h := sha256.New()
	b := make([]byte, 32768)
	r := io.NewSectionReader(f, 0, n)
	for {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		count, e := r.Read(b)
		if count > 0 {
			_, _ = h.Write(b[:count])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func canonical(s string) bool {
	v, e := filepath.EvalSymlinks(s)
	return e == nil && filepath.IsAbs(s) && filepath.Clean(s) == s && v == s
}
func directory(path string, empty bool) (node, error) {
	n, e := named(path)
	if e != nil {
		return n, e
	}
	if !canonical(path) || n.UID != uint32(os.Getuid()) || os.FileMode(n.Mode) != os.ModeDir|0500 {
		return n, errors.New("filter_directory_invalid")
	}
	if empty {
		f, e := os.Open(path)
		if e != nil {
			return n, e
		}
		entries, e := f.ReadDir(1)
		_ = f.Close()
		if len(entries) != 0 || e != io.EOF {
			return n, errors.New("filter_directory_not_empty")
		}
	}
	return n, nil
}
func protectedTool(ctx context.Context, t Tool) (*os.File, node, node, error) {
	fail := func(e error) (*os.File, node, node, error) { return nil, node{}, node{}, e }
	if !canonical(t.Path) || !digestValid(t.SHA256) || t.Size < 4 || t.Size > MaximumToolBytes {
		return fail(errors.New("filter_tool_invalid"))
	}
	parent, e := directory(filepath.Dir(t.Path), false)
	if e != nil {
		return fail(e)
	}
	fd, e := unix.Open(t.Path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return fail(e)
	}
	f := os.NewFile(uintptr(fd), t.Path)
	n, e := identity(f)
	got, ne := named(t.Path)
	if e != nil || ne != nil || n != got || n.UID != uint32(os.Getuid()) || n.Nlink != 1 || os.FileMode(n.Mode) != 0500 || n.Size != t.Size || !process.NativeExecutableFile(f, n.Size) {
		f.Close()
		return fail(errors.New("filter_tool_invalid"))
	}
	hash, e := hashFD(ctx, f, n.Size)
	if e != nil || hash != t.SHA256 {
		f.Close()
		return fail(errors.New("filter_tool_pin_changed"))
	}
	for _, source := range []string{"/usr/bin/env", "/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh", "/bin/csh", "/bin/tcsh"} {
		sha, e := process.ExecutableDigest(ctx, source)
		if e == nil && sha == t.SHA256 {
			f.Close()
			return fail(errors.New("filter_dispatcher_unsupported"))
		}
	}
	return f, n, parent, nil
}
func profile(tool, empty string) string {
	return strings.Join([]string{
		"(version 1)", "(allow default)", "(deny file-read*)", "(deny file-write*)", "(deny file-link)", "(deny network*)", "(deny process-fork)", "(deny process-exec)",
		"(allow process-exec (literal " + strconv.Quote(tool) + "))",
		`(allow file-read* (subpath "/System") (subpath "/usr/lib"))`,
		`(allow file-read* (subpath "/private/preboot/Cryptexes/OS"))`,
		`(allow file-read-metadata)`, `(allow file-read* file-write* (literal "/dev/null"))`,
		`(allow file-read* (literal "/dev/random") (literal "/dev/urandom"))`,
		`(allow file-read-data (literal "/"))`,
		"(allow file-read* (literal " + strconv.Quote(tool) + ") (literal " + strconv.Quote(empty) + "))",
	}, "\n")
}

type starter func(context.Context, process.Command) (*process.Handle, io.ReadWriteCloser, error)

func Execute(ctx context.Context, r Request) (Result, error) {
	return execute(ctx, r, process.StartDuplex)
}
func execute(ctx context.Context, r Request, start starter) (result Result, retErr error) {
	// Freeze all request values before validation or any callbacks. File descriptors
	// are exclusively held by the caller for this entire invocation, not cloned.
	r.Args = append([]string(nil), r.Args...)
	result.ScopeSHA256 = r.ScopeSHA256
	result.ExitCode = -1
	if r.Operation != "clean" && r.Operation != "smudge" || !digestValid(r.ScopeSHA256) || r.Input == nil || r.Output == nil || len(r.Args) > 16 {
		return result, errors.New("filter_request_invalid")
	}
	count := 0
	for _, a := range r.Args {
		count += len(a)
		if !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
			return result, errors.New("filter_request_invalid")
		}
	}
	if count > 4096 {
		return result, errors.New("filter_request_invalid")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return result, errors.New("filter_deadline_required")
	}
	if e := ctx.Err(); e != nil {
		return result, e
	}
	if max := time.Now().Add(60 * time.Second); deadline.After(max) {
		deadline = max
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	input, e := identity(r.Input)
	if e != nil {
		return result, e
	}
	output, e := identity(r.Output)
	if e != nil {
		return result, e
	}
	flags, e := unix.FcntlInt(r.Output.Fd(), unix.F_GETFL, 0)
	if e != nil {
		return result, e
	}
	if input.Device == output.Device && input.Inode == output.Inode || os.FileMode(input.Mode)&os.ModeType != 0 || input.Size < 0 || input.Size > MaximumBytes || os.FileMode(output.Mode) != 0600 || output.UID != uint32(os.Getuid()) || output.Nlink != 1 || output.Size != 0 || flags&unix.O_ACCMODE != unix.O_RDWR || flags&unix.O_APPEND != 0 {
		return result, errors.New("filter_descriptor_invalid")
	}
	empty, e := directory(r.EmptyDirectory, true)
	if e != nil {
		return result, e
	}
	if filepath.Dir(r.Tool.Path) == r.EmptyDirectory {
		return result, errors.New("filter_directory_invalid")
	}
	tool, toolNode, parent, e := protectedTool(runCtx, r.Tool)
	if e != nil {
		return result, e
	}
	defer tool.Close()
	beforeSHA, e := hashFD(runCtx, r.Input, input.Size)
	if e != nil {
		return result, e
	}
	body, e := json.Marshal(struct {
		Operation, Scope                           string
		Tool                                       Tool
		Args                                       []string
		Input, Output, ToolIdentity, Parent, Empty node
		InputSHA256                                string
	}{r.Operation, r.ScopeSHA256, r.Tool, r.Args, input, output, toolNode, parent, empty, beforeSHA})
	if e != nil {
		return result, e
	}
	sum := sha256.Sum256(body)
	result.RequestSHA256 = hex.EncodeToString(sum[:])
	command := process.Command{Path: "/usr/bin/sandbox-exec", Args: append([]string{"-p", profile(r.Tool.Path, r.EmptyDirectory), r.Tool.Path}, r.Args...), Dir: r.EmptyDirectory, ExactEnv: true, PinnedPath: r.Tool.Path, PinnedSHA256: r.Tool.SHA256, SealedPath: r.Tool.Path, SealedSHA256: r.Tool.SHA256, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + r.EmptyDirectory, "TMPDIR=" + r.EmptyDirectory, "XDG_CACHE_HOME=" + r.EmptyDirectory}}
	h, stream, startErr := start(runCtx, command)
	if h == nil {
		if stream != nil {
			_ = stream.Close()
		}
		return result, startErr
	}
	result.Started = true
	result.Identity = h.Identity()
	type copied struct {
		input bool
		bytes int64
		sha   string
		err   error
	}
	completed := make(chan copied, 2)
	remaining := 0
	if startErr == nil && stream != nil {
		writer, ok := stream.(interface{ CloseWrite() error })
		if !ok {
			startErr = errors.New("filter_closewrite_missing")
		} else {
			remaining = 2
			go func() {
				hash := sha256.New()
				reader := io.NewSectionReader(r.Input, 0, input.Size)
				n, err := io.CopyBuffer(io.MultiWriter(stream, hash), reader, make([]byte, 32768))
				closeErr := writer.CloseWrite()
				completed <- copied{true, n, hex.EncodeToString(hash.Sum(nil)), errors.Join(err, closeErr)}
			}()
			go func() {
				hash := sha256.New()
				buf := make([]byte, 32768)
				var n int64
				var err error
				for {
					var k int
					k, err = stream.Read(buf)
					if k > 0 {
						if n+int64(k) > MaximumBytes {
							err = errors.New("filter_output_limit")
							break
						}
						var wrote int
						wrote, err = r.Output.WriteAt(buf[:k], n)
						if wrote > 0 {
							_, _ = hash.Write(buf[:wrote])
							n += int64(wrote)
						}
						if err != nil {
							break
						}
						if wrote != k {
							err = io.ErrShortWrite
							break
						}
					}
					if err == io.EOF {
						err = nil
						break
					}
					if err != nil {
						break
					}
				}
				completed <- copied{false, n, hex.EncodeToString(hash.Sum(nil)), err}
			}()
		}
	} else if startErr == nil {
		startErr = errors.New("filter_stream_missing")
	}
	consume := func(c copied) {
		if c.input {
			result.InputBytes, result.InputSHA256 = c.bytes, c.sha
		} else {
			result.OutputBytes, result.OutputSHA256 = c.bytes, c.sha
		}
		retErr = errors.Join(retErr, c.err)
	}
	retErr = startErr
	for remaining > 0 && retErr == nil {
		select {
		case c := <-completed:
			remaining--
			consume(c)
		case <-runCtx.Done():
			retErr = runCtx.Err()
		}
	}
	if retErr == nil {
		retErr = h.Wait(runCtx)
	}
	if retErr != nil {
		if stream != nil {
			_ = stream.Close()
		}
		fresh, stop := context.WithTimeout(context.Background(), 10*time.Second)
		stopErr := h.Stop(fresh)
		waitErr := h.Wait(fresh)
		treeErr := h.ConfirmTreeExited()
		stop()
		result.WaitProved, result.TreeProved = waitErr == nil, treeErr == nil
		retErr = errors.Join(retErr, stopErr, waitErr, treeErr)
	} else {
		result.WaitProved = true
		treeErr := h.ConfirmTreeExited()
		result.TreeProved = treeErr == nil
		retErr = errors.Join(retErr, treeErr)
		if stream != nil {
			_ = stream.Close()
		}
	}
	// Stop/Wait/tree precede joining; closed pipes unblock both copy directions.
	for remaining > 0 {
		c := <-completed
		remaining--
		consume(c)
	}
	result.ExitCode = h.ExitCode()
	result.Unknown = !result.WaitProved || !result.TreeProved
	if result.Unknown {
		return result, errors.Join(retErr, process.ErrProcessTreeUnknown)
	}
	// Recheck physical facts even after a known failed execution; none confer
	// deletion rights. Never open an input/output pathname obtained from File.Name.
	finalInput, ie := identity(r.Input)
	finalOutput, oe := identity(r.Output)
	finalTool, te := identity(tool)
	namedTool, ne := named(r.Tool.Path)
	finalParent, pe := directory(filepath.Dir(r.Tool.Path), false)
	finalEmpty, de := directory(r.EmptyDirectory, true)
	if ie != nil || oe != nil || te != nil || ne != nil || pe != nil || de != nil || finalInput != input || finalTool != toolNode || namedTool != toolNode || finalParent != parent || finalEmpty != empty || finalOutput.Device != output.Device || finalOutput.Inode != output.Inode || finalOutput.Nlink != output.Nlink || finalOutput.UID != output.UID || finalOutput.Mode != output.Mode || finalOutput.Size != result.OutputBytes || finalOutput.Size > MaximumBytes {
		retErr = errors.Join(retErr, errors.New("filter_identity_changed"))
	}
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer verifyCancel()
	toolSHA, ve := hashFD(verifyCtx, tool, toolNode.Size)
	if ve != nil || toolSHA != r.Tool.SHA256 {
		retErr = errors.Join(retErr, errors.New("filter_tool_pin_changed"))
	}
	outputSHA, ve := hashFD(verifyCtx, r.Output, result.OutputBytes)
	if ve != nil || outputSHA != result.OutputSHA256 {
		retErr = errors.Join(retErr, errors.New("filter_output_changed"))
	}
	if result.InputBytes != input.Size || result.InputSHA256 != beforeSHA {
		retErr = errors.Join(retErr, errors.New("filter_input_changed"))
	}
	if result.ExitCode != 0 {
		retErr = errors.Join(retErr, fmt.Errorf("filter_exit_nonzero: %d", result.ExitCode))
	}
	result.Accepted = retErr == nil && result.WaitProved && result.TreeProved && result.ExitCode == 0
	return result, retErr
}
