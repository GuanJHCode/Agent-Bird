package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type validationToolJournal struct {
	Version       int    `json:"version"`
	Phase         string `json:"phase"`
	ReservationID string `json:"reservation_id"`
	ScopeSHA256   string `json:"scope_sha256"`
	ToolSHA256    string `json:"tool_sha256"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Size          int64  `json:"size"`
	DirectoryDev  uint64 `json:"directory_dev"`
	DirectoryIno  uint64 `json:"directory_ino"`
	FileDev       uint64 `json:"file_dev,omitempty"`
	FileIno       uint64 `json:"file_ino,omitempty"`
}

func validationToolIdentity(info os.FileInfo) (uint64, uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errors.New("validation_tool_identity_invalid")
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}
func validationSyncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}
func writeValidationToolJournal(dir string, j validationToolJournal) error {
	body, e := json.Marshal(j)
	if e != nil || len(body) > contract.ValidationToolJournalLimit {
		return errors.New("validation_tool_journal_invalid")
	}
	path := filepath.Join(dir, "journal.stage")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(body); e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(path, filepath.Join(dir, "journal.json")); e != nil {
		return e
	}
	return validationSyncDir(dir)
}
func readValidationToolJournal(dir string, g contract.LaunchCommand) (validationToolJournal, error) {
	var j validationToolJournal
	f, e := os.OpenFile(filepath.Join(dir, "journal.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return j, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > contract.ValidationToolJournalLimit {
		return j, errors.New("validation_tool_journal_invalid")
	}
	if _, _, e = validationToolIdentity(info); e != nil {
		return j, e
	}
	body, e := io.ReadAll(io.LimitReader(f, contract.ValidationToolJournalLimit+1))
	if e != nil || json.Unmarshal(body, &j) != nil {
		return j, errors.New("validation_tool_journal_invalid")
	}
	canonical, _ := json.Marshal(j)
	if string(body) != string(canonical) || j.Version != 1 || g.ValidationTool == nil || j.ReservationID != g.ValidationTool.ReservationID || j.ScopeSHA256 != g.ValidationTool.ScopeSHA256 || j.ToolSHA256 != g.ValidationTool.ToolSHA256 || j.Size < 0 || j.Size > g.ValidationTool.MaximumBytes {
		return j, errors.New("validation_tool_journal_invalid")
	}
	switch j.Phase {
	case "intent", "creating", "partial", "sealed", "removed":
	default:
		return j, errors.New("validation_tool_journal_invalid")
	}
	info, e = os.Lstat(dir)
	if e != nil {
		return j, e
	}
	dev, ino, e := validationToolIdentity(info)
	if e != nil || !info.IsDir() || dev != j.DirectoryDev || ino != j.DirectoryIno {
		return j, errors.New("validation_tool_identity_invalid")
	}
	return j, nil
}

// Called only by a live pre-Start refusal/typed proof or original Wait/tree path.
// Cold recovery never calls this function and a journal is never a Wait proof.
func (h *Host) removeValidationTool(g contract.LaunchCommand) (*contract.ValidationToolCleanup, error) {
	if g.ValidationTool == nil {
		return nil, nil
	}
	if h.producerID != g.HostID || contract.RequireValidationTool(g, g.ValidationTool.ToolSHA256) != nil {
		return nil, contract.ErrValidationToolGrant
	}
	root := filepath.Join(h.spoolRoot, ".validation-tools")
	if e := validationOwnedDir(root, 0700, true); e != nil {
		return nil, e
	}
	dir := filepath.Dir(h.validationSealedPath(g))
	info, e := os.Lstat(dir)
	if errors.Is(e, os.ErrNotExist) {
		if e = os.Mkdir(dir, 0700); e != nil {
			return nil, e
		}
		info, e = os.Lstat(dir)
		if e != nil {
			return nil, e
		}
		dev, ino, e := validationToolIdentity(info)
		if e != nil {
			return nil, e
		}
		j := validationToolJournal{Version: 1, Phase: "removed", ReservationID: g.ValidationTool.ReservationID, ScopeSHA256: g.ValidationTool.ScopeSHA256, ToolSHA256: g.ValidationTool.ToolSHA256, DirectoryDev: dev, DirectoryIno: ino}
		if e = writeValidationToolJournal(dir, j); e != nil {
			return nil, e
		}
		if e = os.Chmod(dir, 0500); e != nil {
			return nil, e
		}
		if e = validationSyncDir(root); e != nil {
			return nil, e
		}
		return validationCleanupReceipt(j), nil
	}
	if e != nil {
		return nil, e
	}
	j, e := readValidationToolJournal(dir, g)
	if e != nil {
		return nil, e
	}
	path := h.validationSealedPath(g)
	file, e := os.Lstat(path)
	if j.Phase == "removed" {
		if !errors.Is(e, os.ErrNotExist) {
			return nil, errors.New("validation_tool_removed_conflict")
		}
		return validationCleanupReceipt(j), nil
	}
	if errors.Is(e, os.ErrNotExist) {
		if j.Phase != "intent" || j.FileIno != 0 {
			return nil, errors.New("validation_tool_missing_identity")
		}
	} else {
		if e != nil {
			return nil, e
		}
		dev, ino, e := validationToolIdentity(file)
		st, ok := file.Sys().(*syscall.Stat_t)
		if e != nil || !ok || st.Nlink != 1 || !file.Mode().IsRegular() || file.Mode().Perm() != 0500 || dev != j.FileDev || ino != j.FileIno || file.Size() != j.Size || j.ContentSHA256 == "" {
			return nil, errors.New("validation_tool_delete_identity_invalid")
		}
		digest, e := process.ExecutableDigest(context.Background(), path)
		if e != nil || digest != j.ContentSHA256 {
			return nil, errors.New("validation_tool_delete_pin_changed")
		}
	}
	if e = os.Chmod(dir, 0700); e != nil {
		return nil, e
	}
	if file != nil {
		if e = os.Remove(path); e != nil {
			return nil, e
		}
		if e = validationSyncDir(dir); e != nil {
			return nil, e
		}
	}
	j.Phase = "removed"
	if e = writeValidationToolJournal(dir, j); e != nil {
		return nil, e
	}
	if e = os.Chmod(dir, 0500); e != nil {
		return nil, e
	}
	if e = validationSyncDir(root); e != nil {
		return nil, e
	}
	return validationCleanupReceipt(j), nil
}
func validationCleanupReceipt(j validationToolJournal) *contract.ValidationToolCleanup {
	body, _ := json.Marshal(j)
	return &contract.ValidationToolCleanup{Version: 1, ReservationID: j.ReservationID, ScopeSHA256: j.ScopeSHA256, ToolSHA256: j.ToolSHA256, JournalSHA256: hashBytes(body), RemovedBytes: j.Size}
}
