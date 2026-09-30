package host

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func (h *Host) segmentStopComplete(segmentID string) (bool, error) {
	attempts, err := os.ReadDir(h.spoolRoot)
	if err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if !attempt.IsDir() || filepath.Base(segmentID) != segmentID || segmentID == "." || segmentID == ".." {
			continue
		}
		path := filepath.Join(h.spoolRoot, attempt.Name(), segmentID, "launch.json")
		terminal, err := readStopTerminal(path, attempt.Name(), segmentID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || terminal {
			return terminal, err
		}
	}
	return false, nil
}

func readStopTerminal(path, attemptID, segmentID string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	owner, owned := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64*1024 || !owned || owner.Uid != uint32(os.Geteuid()) {
		return false, errors.New("stop_lifecycle_record_untrusted")
	}
	body, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return false, err
	}
	var record struct {
		Version   int    `json:"version"`
		AttemptID string `json:"attempt_id"`
		SegmentID string `json:"segment_id"`
		Phase     string `json:"phase"`
	}
	if len(body) > 64*1024 || json.Unmarshal(body, &record) != nil || record.Version != 1 || record.AttemptID != attemptID || record.SegmentID != segmentID {
		return false, errors.New("stop_lifecycle_record_invalid")
	}
	return record.Phase == "exited" || record.Phase == "unknown", nil
}
