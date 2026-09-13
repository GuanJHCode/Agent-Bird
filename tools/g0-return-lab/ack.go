package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

var safeHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type control struct {
	Version          int    `json:"version"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	Cancelled        bool   `json:"cancelled"`
}

type ackRecord struct {
	Version          int    `json:"version"`
	Status           string `json:"status"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	Revision         int    `json:"revision"`
	EventHash        string `json:"event_hash"`
	CommandID        string `json:"command_id"`
	Decision         string `json:"decision"`
	DecisionCount    int    `json:"decision_count"`
	EffectCount      int    `json:"effect_count"`
}

// This canonical field order is the event hash contract. The task revision is
// immutable producer metadata, never the controller's later current revision.
type eventEnvelope struct {
	Version          int    `json:"version"`
	Nonce            string `json:"nonce"`
	ControllerThread string `json:"controller_thread"`
	TaskRevision     int    `json:"task_revision"`
	Result           result `json:"result"`
}

type inspection struct {
	Version          int        `json:"version"`
	Status           string     `json:"status"`
	Nonce            string     `json:"nonce"`
	ControllerThread string     `json:"controller_thread"`
	Revision         int        `json:"revision"`
	TaskRevision     int        `json:"task_revision"`
	Cancelled        bool       `json:"cancelled"`
	EventHash        string     `json:"event_hash"`
	Result           result     `json:"result"`
	ACK              *ackRecord `json:"ack"`
}

func validRevision(revision int) bool { return revision >= 1 && revision <= 1000000 }

func validDecision(decision string) bool {
	return decision == "handled" || decision == "waiting_user" || decision == "stale" || decision == "rejected"
}

func effectCount(decision string) int {
	if decision == "handled" {
		return 1
	}
	return 0
}

func initializeControl(root *os.Root, o options) error {
	if err := writeNew(root, "control.lock", struct {
		Version int `json:"version"`
	}{1}); err != nil {
		return err
	}
	return withControlLock(root, func() error {
		return publish(root, "control.json", control{1, o.nonce, o.controller, 1, false})
	})
}

// All inspect/ACK/revise operations use this same stable inode. We never replace
// or delete control.lock. A process death releases flock through the OS.
func withControlLock(root *os.Root, action func() error) error {
	f, err := root.OpenFile("control.lock", os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return failure("control_missing")
		}
		return failure("untrusted_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !owned(info, 0600) || info.Size() > 4096 {
		return failure("untrusted_file")
	}
	if stat := info.Sys().(*syscall.Stat_t); stat.Nlink != 1 {
		return failure("untrusted_file")
	}
	fd := int(f.Fd())
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return failure("lock_failed")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return failure("lock_timeout")
		}
		time.Sleep(min(50*time.Millisecond, remaining))
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	current, err := root.Lstat("control.lock")
	if err != nil || !owned(current, 0600) || !os.SameFile(info, current) {
		return failure("untrusted_file")
	}
	return action()
}

func readControl(root *os.Root, nonce string) (control, error) {
	var c control
	if err := readJSON(root, "control.json", &c); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, failure("control_missing")
		}
		return c, err
	}
	if c.Nonce != nonce {
		return c, failure("nonce_mismatch")
	}
	if c.Version != 1 || !safeNonce.MatchString(c.ControllerThread) || !validRevision(c.Revision) {
		return c, failure("invalid_artifact")
	}
	return c, nil
}

func boundJob(root *os.Root, c control) (job, error) {
	j, err := readJob(root, c.Nonce)
	if err != nil {
		return j, err
	}
	if j.ControllerThread != c.ControllerThread || j.TaskRevision != 1 || c.Revision < j.TaskRevision {
		return j, failure("invalid_artifact")
	}
	return j, nil
}

func boundResult(root *os.Root, c control) (job, result, string, error) {
	j, err := boundJob(root, c)
	if err != nil {
		return j, result{}, "", err
	}
	r, err := readResult(root, c.Nonce)
	if err != nil || r.Status != "completed" {
		return j, r, "", err
	}
	data, err := json.Marshal(eventEnvelope{1, c.Nonce, j.ControllerThread, j.TaskRevision, r})
	if err != nil {
		return j, r, "", err
	}
	hash := sha256.Sum256(data)
	return j, r, hex.EncodeToString(hash[:]), nil
}

func ackName(revision int) string { return "ack-r" + strconv.Itoa(revision) + ".json" }

func readACK(root *os.Root, c control, j job, hash string) (ackRecord, error) {
	var a ackRecord
	if err := readJSON(root, ackName(j.TaskRevision), &a); err != nil {
		return a, err
	}
	if a.Version != 1 || a.Status != "acknowledged" || a.Nonce != c.Nonce || a.ControllerThread != c.ControllerThread || a.Revision != j.TaskRevision || a.EventHash != hash || !safeHash.MatchString(a.EventHash) || !safeNonce.MatchString(a.CommandID) || !validDecision(a.Decision) || a.DecisionCount != 1 || a.EffectCount != effectCount(a.Decision) {
		return a, failure("invalid_artifact")
	}
	return a, nil
}

// A partial stage is not an effect. Under the shared lock only, discard a trusted
// bounded stage from an interrupted operation. Published destinations stay intact.
func clearUnpublishedStage(root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return failure("untrusted_file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !owned(info, 0600) || info.Size() > 4096 {
		return failure("untrusted_file")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return failure("untrusted_file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Nlink != 1 && stat.Nlink != 2) {
		return failure("untrusted_file")
	}
	if stat.Nlink == 2 {
		if len(name) < 6 || name[0] != '.' || name[len(name)-4:] != ".tmp" {
			return failure("untrusted_file")
		}
		destination := name[1 : len(name)-4]
		paired, pairErr := root.Lstat(destination)
		if pairErr != nil || !owned(paired, 0600) || !os.SameFile(info, paired) {
			return failure("untrusted_file")
		}
	}
	return root.Remove(name)
}

func controlCommand(root *os.Root, o options) (any, error) {
	var output any
	err := withControlLock(root, func() error {
		c, err := readControl(root, o.nonce)
		if err != nil {
			return err
		}
		if o.sub != "inspect" {
			if c.ControllerThread != o.controller {
				return failure("owner_mismatch")
			}
			if c.Cancelled {
				return failure("cancelled")
			}
			if c.Revision != o.revision {
				return failure("stale_revision")
			}
		}
		var j job
		var r result
		var hash string
		if o.sub == "revise" {
			j, err = boundJob(root, c)
		} else {
			j, r, hash, err = boundResult(root, c)
		}
		if err != nil {
			return err
		}
		switch o.sub {
		case "inspect":
			i := inspection{1, r.Status, c.Nonce, c.ControllerThread, c.Revision, j.TaskRevision, c.Cancelled, hash, r, nil}
			a, err := readACK(root, c, j, hash)
			if err == nil {
				i.ACK = &a
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			output = i
			return nil
		case "revise":
			if !validRevision(c.Revision + 1) {
				return failure("revision_limit")
			}
			c.Revision++
			c.Cancelled = o.cancel
			stage := ".control-revise.tmp"
			if err := clearUnpublishedStage(root, stage); err != nil {
				return err
			}
			if err := writeNew(root, stage, c); err != nil {
				return failure("revise_uncertain")
			}
			defer root.Remove(stage)
			// control.json is the sole deliberately mutable record. ACK/effect records
			// and producer metadata are never replaced by this operation.
			if err := root.Rename(stage, "control.json"); err != nil {
				return failure("revise_uncertain")
			}
			if err := syncDir(root); err != nil {
				return failure("revise_uncertain")
			}
			output = c
			return nil
		case "ack":
			if r.Status != "completed" {
				return failure("result_pending")
			}
			if j.TaskRevision != o.revision {
				return failure("event_revision_mismatch")
			}
			if hash != o.eventHash {
				return failure("event_hash_mismatch")
			}
			name := ackName(j.TaskRevision)
			existing, err := readACK(root, c, j, hash)
			if err == nil {
				if existing.Decision != o.decision {
					return failure("decision_conflict")
				}
				if err := clearUnpublishedStage(root, "."+name+".tmp"); err != nil {
					return err
				}
				output = existing
				return nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := clearUnpublishedStage(root, "."+name+".tmp"); err != nil {
				return err
			}
			a := ackRecord{1, "acknowledged", c.Nonce, c.ControllerThread, j.TaskRevision, hash, o.commandID, o.decision, 1, effectCount(o.decision)}
			// Publication is the synthetic effect: ACK and count commit together.
			if err := publish(root, name, a); err != nil {
				return failure("ack_uncertain")
			}
			output = a
			return nil
		}
		return failure("invalid_args")
	})
	return output, err
}
