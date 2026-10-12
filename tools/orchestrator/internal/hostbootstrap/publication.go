//go:build !windows

package hostbootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const publicationDirectoryLimit = 4096

type publicationCheckpoint func(string) error

func checkpoint(h publicationCheckpoint, point string) error {
	if h != nil {
		return h(point)
	}
	return nil
}
func publicationHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func publicationDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func (l *Lease) publicationName(expected []byte) string {
	return ".publication-" + publicationHash([]byte(l.launchID)) + "-" + publicationHash(expected)
}
func (l *Lease) publicationBytes(expected []byte) ([]byte, error) {
	if l == nil || len(l.paths) != 3 || len(l.dirs) != 3 || l.lock == nil {
		return nil, Error("host_launch_lease_changed")
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	if len(expected) < 1 || len(expected) > MaxBytes {
		return nil, Error("invalid_host_bootstrap")
	}
	raw := append([]byte(nil), expected...)
	var m MetadataV2
	if len(raw) < 1 || len(raw) > MaxBytes || decodeMetadata(raw, &m) != nil || m.Version != 2 || m.OwnerLaunchProtocol != 0 && m.OwnerLaunchProtocol != 2 || m.ProducerID != l.launchID || m.Hello.LaunchID != l.launchID || m.SocketPath != l.paths[0]+"/coordinator.sock" || m.SpoolRoot != l.paths[0]+"/host-spool/"+l.launchID {
		return nil, Error("invalid_host_bootstrap")
	}
	return raw, nil
}

// Snapshot copies the original bytes selected by Read, never re-marshals them.
func (l *Lease) Snapshot() ([]byte, error) {
	if err := l.Unchanged(); err != nil {
		return nil, err
	}
	return append([]byte(nil), l.bootstrapBytes...), nil
}

// publicationStages permits only an attributed, exact journal stage. Old
// un-attributed and unknown publication names are retained and fail closed.
func (l *Lease) publicationStages(expected []byte, replay bool) (bool, error) {
	if err := l.validate(); err != nil {
		return false, err
	}
	dir, err := launchOpenAt(l.dirs[2], ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(publicationDirectoryLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if len(names) > publicationDirectoryLimit {
		return false, Error("host_bootstrap_publication_stage_limit")
	}
	wanted := l.publicationName(expected)
	launchHash := publicationHash([]byte(l.launchID))
	present := false
	for _, name := range names {
		if strings.HasPrefix(name, "."+l.name+".replace-") {
			return false, Error("host_bootstrap_publication_stage_invalid")
		}
		if !strings.HasPrefix(name, ".publication-") {
			continue
		}
		suffix := strings.TrimPrefix(name, ".publication-")
		if len(suffix) != 129 || suffix[64] != '-' || !publicationDigest(suffix[:64]) || !publicationDigest(suffix[65:]) {
			return false, Error("host_bootstrap_publication_stage_invalid")
		}
		if suffix[:64] != launchHash {
			continue
		}
		if !replay || name != wanted || present {
			return false, Error("host_bootstrap_publication_stage_invalid")
		}
		present = true
	}
	return present, l.validate()
}

// stagePrefix bounds the read after Stat and rechecks FD/named identity. Growth
// cannot reach a slice operation before both length and drift checks pass.
func (l *Lease) stagePrefix(f *os.File, expected []byte, h publicationCheckpoint) ([]byte, os.FileInfo, error) {
	if err := l.validate(); err != nil {
		return nil, nil, err
	}
	before, err := f.Stat()
	if err != nil || !launchPrivateFile(before) || before.Size() < 0 || before.Size() > int64(len(expected)) {
		return nil, nil, Error("host_bootstrap_changed")
	}
	if err = checkpoint(h, "stage_stated"); err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, MaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	named, e := launchOpenAt(l.dirs[2], f.Name(), unix.O_RDONLY, 0)
	if e != nil {
		return nil, nil, Error("host_bootstrap_changed")
	}
	namedInfo, e := named.Stat()
	_ = named.Close()
	if err != nil || e != nil || !launchPrivateFile(after) || !launchPrivateFile(namedInfo) || !os.SameFile(before, after) || !os.SameFile(after, namedInfo) || before.Size() != after.Size() || after.Size() != int64(len(raw)) || namedInfo.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(namedInfo.ModTime()) || len(raw) > len(expected) {
		return nil, nil, Error("host_bootstrap_changed")
	}
	if !bytes.Equal(raw, expected[:len(raw)]) {
		return nil, nil, Error("host_bootstrap_changed")
	}
	if err = l.validate(); err != nil {
		return nil, nil, err
	}
	return raw, after, nil
}
func (l *Lease) PreparePublication(expected []byte, replay bool) error {
	l.publicationExpected = nil
	raw, err := l.publicationBytes(expected)
	if err != nil {
		return err
	}
	if err = l.Unchanged(); err != nil {
		return err
	}
	present, err := l.publicationStages(raw, replay)
	if err != nil {
		return err
	}
	if present {
		if bytes.Equal(l.bootstrapBytes, raw) {
			return Error("host_bootstrap_publication_stage_invalid")
		}
		stage, e := launchOpenAt(l.dirs[2], l.publicationName(raw), unix.O_RDONLY, 0)
		if e != nil {
			return e
		}
		_, _, e = l.stagePrefix(stage, raw, nil)
		_ = stage.Close()
		if e != nil {
			return e
		}
	}
	if err = l.Unchanged(); err != nil {
		return err
	}
	l.publicationExpected = raw
	l.publicationReplay = replay
	return nil
}
func (l *Lease) publicationCurrent(expected []byte, priorSHA string) error {
	if !publicationDigest(priorSHA) {
		return Error("invalid_host_bootstrap")
	}
	if err := l.Unchanged(); err != nil {
		return err
	}
	if !bytes.Equal(l.bootstrapBytes, expected) && publicationHash(l.bootstrapBytes) != priorSHA {
		return Error("host_bootstrap_changed")
	}
	return nil
}
func (l *Lease) PublishPublication(expected []byte, priorSHA string) error {
	return l.publishPublication(expected, priorSHA, nil)
}
func (l *Lease) publishPublication(expected []byte, priorSHA string, h publicationCheckpoint) error {
	raw, err := l.publicationBytes(expected)
	if err != nil {
		return err
	}
	if len(l.publicationExpected) == 0 || !bytes.Equal(raw, l.publicationExpected) {
		return Error("host_bootstrap_publication_not_prepared")
	}
	if err = l.publicationCurrent(raw, priorSHA); err != nil {
		return err
	}
	present, err := l.publicationStages(raw, l.publicationReplay)
	if err != nil {
		return err
	}
	if bytes.Equal(l.bootstrapBytes, raw) {
		if present {
			return Error("host_bootstrap_publication_stage_invalid")
		}
		return l.VerifyPublication(raw)
	}
	name := l.publicationName(raw)
	dir := l.dirs[2]
	created := false
	renamed := false
	f, err := launchOpenAt(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		if !errors.Is(err, unix.EEXIST) || !l.publicationReplay || !present {
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
	identity, err := f.Stat()
	if err != nil {
		return err
	}
	defer func() {
		// Ordinary failures may remove only our newly-created private inode. A
		// replay inode, replacement or root drift remains diagnostic evidence.
		if !created || renamed || l.validate() != nil {
			return
		}
		named, e := launchOpenAt(dir, name, unix.O_RDONLY, 0)
		if e != nil {
			return
		}
		defer named.Close()
		current, e := named.Stat()
		held, he := f.Stat()
		if e == nil && he == nil && launchPrivateFile(current) && launchPrivateFile(held) && os.SameFile(identity, current) && os.SameFile(current, held) {
			_ = unix.Unlinkat(int(dir.Fd()), name, 0)
			_ = dir.Sync()
		}
	}()
	if created {
		if err = checkpoint(h, "created"); err != nil {
			return err
		}
	}
	prefix, stageInfo, err := l.stagePrefix(f, raw, h)
	if err != nil || !os.SameFile(identity, stageInfo) {
		if err != nil {
			return err
		}
		return Error("host_bootstrap_changed")
	}
	remaining := raw[len(prefix):]
	if len(remaining) > 0 {
		first := (len(remaining) + 1) / 2
		n, e := f.WriteAt(remaining[:first], int64(len(prefix)))
		if e != nil {
			return e
		}
		if n != first {
			return io.ErrShortWrite
		}
		if err = checkpoint(h, "partial_write"); err != nil {
			return err
		}
		partial, info, e := l.stagePrefix(f, raw, nil)
		if e != nil {
			return e
		}
		if len(partial) != len(prefix)+first || !os.SameFile(identity, info) {
			return Error("host_bootstrap_changed")
		}
		if first < len(remaining) {
			n, e = f.WriteAt(remaining[first:], int64(len(prefix)+first))
			if e != nil {
				return e
			}
			if n != len(remaining)-first {
				return io.ErrShortWrite
			}
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = checkpoint(h, "file_synced"); err != nil {
		return err
	}
	if err = l.publicationCurrent(raw, priorSHA); err != nil {
		return err
	}
	staged, info, err := l.stagePrefix(f, raw, nil)
	if err != nil {
		return err
	}
	if !os.SameFile(identity, info) || !bytes.Equal(staged, raw) {
		return Error("host_bootstrap_changed")
	}
	if _, err = l.publicationStages(raw, true); err != nil {
		return err
	}
	if err = checkpoint(h, "pre_rename"); err != nil {
		return err
	}
	// Test checkpoints and external writers cannot bypass the final barriers.
	if err = l.publicationCurrent(raw, priorSHA); err != nil {
		return err
	}
	staged, info, err = l.stagePrefix(f, raw, nil)
	if err != nil {
		return err
	}
	if !os.SameFile(identity, info) || !bytes.Equal(staged, raw) {
		return Error("host_bootstrap_changed")
	}
	if _, err = l.publicationStages(raw, true); err != nil {
		return err
	}
	if err = unix.Renameat(int(dir.Fd()), name, int(dir.Fd()), l.name); err != nil {
		return err
	}
	renamed = true
	body, final, err := l.readBytes()
	if err != nil {
		return err
	}
	if !os.SameFile(identity, final) || !bytes.Equal(body, raw) {
		return Error("host_bootstrap_changed")
	}
	l.bootstrapInfo = final
	l.bootstrapBytes = append([]byte(nil), body...)
	return l.VerifyPublication(raw)
}

// VerifyPublication is the durable file/dir and absence barrier used before
// Store finalization and again before returning a published receipt.
func (l *Lease) VerifyPublication(expected []byte) error {
	raw, err := l.publicationBytes(expected)
	if err != nil {
		return err
	}
	if err = l.Unchanged(); err != nil {
		return err
	}
	if !bytes.Equal(raw, l.bootstrapBytes) {
		return Error("host_bootstrap_changed")
	}
	present, err := l.publicationStages(raw, true)
	if err != nil {
		return err
	}
	if present {
		return Error("host_bootstrap_publication_stage_invalid")
	}
	f, err := launchOpenAt(l.dirs[2], l.name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !launchPrivateFile(info) || !os.SameFile(info, l.bootstrapInfo) {
		return Error("host_bootstrap_changed")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = l.dirs[2].Sync(); err != nil {
		return err
	}
	body, after, err := l.readBytes()
	if err != nil {
		return err
	}
	held, e := f.Stat()
	if e != nil || !launchPrivateFile(held) || !os.SameFile(info, after) || !os.SameFile(held, after) || !bytes.Equal(body, raw) {
		return Error("host_bootstrap_changed")
	}
	present, err = l.publicationStages(raw, true)
	if err != nil {
		return err
	}
	if present {
		return Error("host_bootstrap_publication_stage_invalid")
	}
	return l.Unchanged()
}
