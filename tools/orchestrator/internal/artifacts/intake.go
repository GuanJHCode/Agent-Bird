//go:build darwin || linux

// Package artifacts materializes Store-authorized immutable objects. Descriptor
// validation is not authorization: only the Store may issue an intake identity.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const MaxBytes int64 = 1 << 20
const manifestBytes = 16 << 10

var (
	ErrUnsafe   = errors.New("artifact_intake_unsafe")
	ErrConflict = errors.New("artifact_intake_conflict")
	ErrContent  = errors.New("artifact_intake_content_mismatch")
)

type Intake struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	SourceRoot string `json:"source_root"`
	SourcePath string `json:"source_path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

type Proof struct {
	IntakeID         string `json:"intake_id"`
	DescriptorSHA256 string `json:"descriptor_sha256"`
	Path             string `json:"path"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	Device           uint64 `json:"device"`
	Inode            uint64 `json:"inode"`
	RootDevice       uint64 `json:"root_device"`
	RootInode        uint64 `json:"root_inode"`
	ParentDevice     uint64 `json:"parent_device"`
	ParentInode      uint64 `json:"parent_inode"`
	DirectoryDevice  uint64 `json:"directory_device"`
	DirectoryInode   uint64 `json:"directory_inode"`
	IntentDevice     uint64 `json:"intent_device"`
	IntentInode      uint64 `json:"intent_inode"`
	LockDevice       uint64 `json:"lock_device"`
	LockInode        uint64 `json:"lock_inode"`
}

func (s Intake) Validate() error {
	_, err := s.canonical()
	return err
}

func token(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s Intake) canonical() ([]byte, error) {
	if s.Version != 1 || !token(s.ID) || s.Size < 0 || s.Size > MaxBytes || len(s.SHA256) != 64 || strings.ToLower(s.SHA256) != s.SHA256 {
		return nil, ErrUnsafe
	}
	if b, err := hex.DecodeString(s.SHA256); err != nil || len(b) != sha256.Size {
		return nil, ErrUnsafe
	}
	for _, path := range []string{s.SourceRoot, s.SourcePath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 {
			return nil, ErrUnsafe
		}
	}
	rel, err := filepath.Rel(s.SourceRoot, s.SourcePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return nil, ErrUnsafe
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > manifestBytes {
		return nil, ErrUnsafe
	}
	return raw, nil
}

func Materialize(ctx context.Context, stateDir string, s Intake) (Proof, error) {
	return materialize(ctx, stateDir, s, nil)
}

// VerifyAndCommit reopens and verifies the original physical object, then holds
// its lifetime lock while the caller performs its short authenticated commit.
// The callback must keep its transaction short and must not reopen, mutate or
// acquire a second lock on this object. Admission may still inspect disk space.
// This operation cannot create directories, locks, descriptors or data.
func VerifyAndCommit(ctx context.Context, stateDir string, s Intake, expected Proof, commit func() error) (err error) {
	if err = s.Validate(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	n, err := openObjectMode(stateDir, s.ID, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, n.close()) }()
	if err = n.lock(ctx); err != nil {
		return err
	}
	if err = n.layout(s.Size); err != nil {
		return err
	}
	for _, name := range []string{"intent.stage", "data.stage"} {
		if _, statErr := n.stat(name); !errors.Is(statErr, unix.ENOENT) {
			return errors.Join(ErrUnsafe, statErr)
		}
	}
	actual, err := n.proof(ctx, s)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrUnsafe
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return commit()
}

func materialize(ctx context.Context, stateDir string, s Intake, step func(string) error) (proof Proof, err error) {
	raw, err := s.canonical()
	if err != nil {
		return proof, err
	}
	if err = ctx.Err(); err != nil {
		return proof, err
	}
	n, err := openObject(stateDir, s.ID)
	if err != nil {
		return proof, err
	}
	defer func() {
		err = errors.Join(err, n.close())
		if err != nil {
			proof = Proof{}
		}
	}()
	n.step = step
	if err = n.lock(ctx); err != nil {
		return proof, err
	}
	if err = n.layout(s.Size); err != nil {
		return proof, err
	}
	if err = n.ensureIntent(ctx, raw); err != nil {
		return proof, err
	}
	if _, statErr := n.stat("data.bin"); statErr == nil {
		// A published object is the durable retry source. Provider files may
		// already be gone and must not be reopened on this path.
		if _, e := n.stat("data.stage"); !errors.Is(e, unix.ENOENT) {
			return proof, ErrUnsafe
		}
		if err = n.sync(); err != nil {
			return proof, err
		}
		return n.proof(ctx, s)
	} else if !errors.Is(statErr, unix.ENOENT) {
		return proof, statErr
	}
	ready, err := n.recoverStage(ctx, s)
	if err != nil {
		return proof, err
	}
	if !ready {
		err = copySource(ctx, n, s)
	}
	if err != nil {
		return proof, err
	}
	if err = n.publish("data.stage", "data.bin"); err != nil {
		return proof, err
	}
	return n.proof(ctx, s)
}

func copySource(ctx context.Context, n *object, s Intake) (err error) {
	source, err := openChain(s.SourceRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.close()) }()
	rel, _ := filepath.Rel(s.SourceRoot, s.SourcePath)
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if err = source.child(part, false); err != nil {
			return err
		}
	}
	name := parts[len(parts)-1]
	in, before, err := source.regular(name, unix.O_RDONLY, s.Size, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	if before.Size != s.Size {
		return ErrContent
	}
	out, stageID, err := n.regular("data.stage", unix.O_RDWR, s.Size, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if err = n.point("data.stage:before-truncate"); err != nil {
		return err
	}
	if err = n.verify(out, "data.stage", stageID, false, s.Size); err != nil {
		return err
	}
	if err = out.Truncate(0); err != nil {
		return err
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var written int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		count, readErr := in.Read(buffer)
		if count > 0 {
			if int64(count) > s.Size-written {
				return ErrContent
			}
			if _, err = out.Write(buffer[:count]); err != nil {
				return err
			}
			_, _ = hash.Write(buffer[:count])
			written += int64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if written != s.Size || hex.EncodeToString(hash.Sum(nil)) != s.SHA256 {
		return ErrContent
	}
	if err = n.point("data.stage:after-write"); err != nil {
		return err
	}
	if err = source.verify(in, name, before, true, s.Size); err != nil {
		return err
	}
	if err = n.verify(out, "data.stage", stageID, false, s.Size); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return n.point("data.stage:after-sync")
}

func (n *object) proof(ctx context.Context, s Intake) (Proof, error) {
	want, err := s.canonical()
	if err != nil {
		return Proof{}, err
	}
	intentBefore, err := n.stat("intent.json")
	if err != nil {
		return Proof{}, err
	}
	got, err := n.read("intent.json", manifestBytes)
	if err != nil || string(got) != string(want) {
		return Proof{}, errors.Join(ErrConflict, err)
	}
	before, err := n.verifyData(ctx, "data.bin", s)
	if err != nil {
		return Proof{}, err
	}
	intentAfter, err := n.stat("intent.json")
	if err != nil || !state(intentBefore, intentAfter) {
		return Proof{}, errors.Join(ErrUnsafe, err)
	}
	if err = n.verify(n.lockFile, "writer.lock", n.lockID, true, 0); err != nil {
		return Proof{}, err
	}
	digest := sha256.Sum256(want)
	return Proof{IntakeID: s.ID, DescriptorSHA256: hex.EncodeToString(digest[:]), Path: filepath.Join(n.path, "data.bin"), SHA256: s.SHA256, Size: s.Size, Device: uint64(before.Dev), Inode: uint64(before.Ino),
		RootDevice: uint64(n.ids[0].Dev), RootInode: uint64(n.ids[0].Ino), ParentDevice: uint64(n.ids[1].Dev), ParentInode: uint64(n.ids[1].Ino), DirectoryDevice: uint64(n.ids[2].Dev), DirectoryInode: uint64(n.ids[2].Ino),
		IntentDevice: uint64(intentBefore.Dev), IntentInode: uint64(intentBefore.Ino), LockDevice: uint64(n.lockID.Dev), LockInode: uint64(n.lockID.Ino)}, nil
}

func (n *object) recoverStage(ctx context.Context, s Intake) (bool, error) {
	before, err := n.verifyData(ctx, "data.stage", s)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, ErrContent) {
		// Unpublished scratch may be incomplete. It is replaced only after
		// reopening the same descriptor's exact source, never another path.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f, _, err := n.regular("data.stage", unix.O_RDONLY, s.Size, false)
	if err != nil {
		return false, err
	}
	checkErr := n.verify(f, "data.stage", before, true, s.Size)
	if checkErr == nil {
		checkErr = f.Sync()
	}
	if checkErr == nil {
		checkErr = n.verify(f, "data.stage", before, true, s.Size)
	}
	return true, errors.Join(checkErr, f.Close())
}

func (n *object) verifyData(ctx context.Context, name string, s Intake) (before unix.Stat_t, err error) {
	f, before, err := n.regular(name, unix.O_RDONLY, s.Size, false)
	if err != nil {
		return before, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if before.Size != s.Size {
		return before, ErrContent
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return before, err
		}
		count, readErr := f.Read(buffer)
		if int64(count) > s.Size-total {
			return before, ErrContent
		}
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
			total += int64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return before, readErr
		}
	}
	if total != s.Size || hex.EncodeToString(hash.Sum(nil)) != s.SHA256 {
		return before, ErrContent
	}
	if err = n.verify(f, name, before, true, s.Size); err != nil {
		return before, err
	}
	return before, nil
}
