//go:build darwin || linux

package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var (
	ErrClosed        = errors.New("diagnostics_closed")
	ErrRecoveryGap   = errors.New("diagnostics_recovery_gap_required")
	ErrInvalidCursor = errors.New("diagnostics_invalid_cursor")
	ErrOverflow      = errors.New("diagnostics_sequence_overflow")
)

type operationGate chan struct{}

func newGate() operationGate { g := make(operationGate, 1); g <- struct{}{}; return g }
func (g operationGate) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g:
	}
	if err := ctx.Err(); err != nil {
		g.leave()
		return err
	}
	return nil
}
func (g operationGate) leave() { g <- struct{}{} }

func (n *namespace) hasFile(name string) (bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(n.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	limit, ok := fileLimit(name)
	if !ok || !privateStat(st, false) || st.Size > int64(limit) {
		return false, ErrUnsafeLayout
	}
	return true, nil
}

func matchesDescriptor(raw []byte, d descriptor) bool {
	if d.Sequence == 0 || len(raw) != d.Size {
		return false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == d.SHA256
}

func descriptorFor(sequence uint64, raw []byte) descriptor {
	sum := sha256.Sum256(raw)
	return descriptor{Sequence: sequence, Size: len(raw), SHA256: hex.EncodeToString(sum[:])}
}

func (n *namespace) readChunk(d descriptor, buffer []byte, scopeHash string) (chunk, error) {
	raw, err := n.readInto(slotName(int((d.Sequence-1)%SlotCount)), ChunkBytes, buffer)
	if err != nil {
		return chunk{}, err
	}
	if !matchesDescriptor(raw, d) {
		return chunk{}, ErrCorrupt
	}
	c, err := decodeChunk(raw)
	if err != nil {
		return chunk{}, err
	}
	if c.scopeHash != scopeHash || c.sequence != d.Sequence {
		return chunk{}, ErrCorrupt
	}
	return c, nil
}

// loadStored validates all retained bytes using one reusable raw buffer. Its
// returned last chunk borrows that buffer. Pending state is resolved in memory;
// only OpenWriter may publish the resolution under an exclusive update lock.
func (n *namespace) loadStored(ctx context.Context, scope Scope, buffer []byte) (manifest, chunk, bool, error) {
	var m manifest
	if err := n.checkLayout(); err != nil {
		return m, chunk{}, false, err
	}
	raw, err := n.readFile("manifest.json", ManifestBytes)
	if err != nil {
		return m, chunk{}, false, err
	}
	m, err = decodeManifest(raw, scope)
	if err != nil {
		return m, chunk{}, false, err
	}
	manifestStage, err := n.hasFile(".manifest.stage")
	if err != nil {
		return m, chunk{}, false, err
	}
	chunkStage, err := n.hasFile("chunk.stage")
	if err != nil {
		return m, chunk{}, false, err
	}
	unclean := !m.Closed || m.Pending != nil || manifestStage || chunkStage
	if p := m.Pending; p != nil {
		target, readErr := n.readInto(slotName(p.Index), ChunkBytes, buffer)
		newTarget := readErr == nil && matchesDescriptor(target, p.New)
		oldTarget := (readErr == nil && matchesDescriptor(target, p.Old)) || (errors.Is(readErr, os.ErrNotExist) && p.Old.Sequence == 0)
		if !newTarget && !oldTarget {
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return m, chunk{}, false, readErr
			}
			return m, chunk{}, false, ErrCorrupt
		}
		m.applyPending(newTarget)
	}
	for i, d := range m.Slots {
		present, err := n.hasFile(slotName(i))
		if err != nil {
			return m, chunk{}, false, err
		}
		if present != (d.Sequence != 0) {
			return m, chunk{}, false, ErrCorrupt
		}
	}
	hash, _ := scope.Hash()
	first, last, count := m.window()
	var latest chunk
	var ranges recordRanges
	segment := ""
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return m, chunk{}, false, err
		}
		seq := first + uint64(index)
		latest, err = n.readChunk(m.Slots[(seq-1)%SlotCount], buffer, hash)
		if err != nil {
			return m, chunk{}, false, err
		}
		if segment != latest.segmentID {
			segment = latest.segmentID
			ranges = recordRanges{}
		}
		it := latest.Iterator()
		for {
			record, ok, err := it.Next()
			if err != nil {
				return m, chunk{}, false, err
			}
			if !ok {
				break
			}
			if err = ranges.accept(record.Stream, record.Offset, record.LostBefore, len(record.Data)); err != nil {
				return m, chunk{}, false, err
			}
		}
	}
	if last != 0 {
		if m.State.SegmentID != segment {
			return m, chunk{}, false, ErrCorrupt
		}
		for i, p := range m.State.Streams {
			if ranges.seen[i+1] && (!p.Seen || p.End != ranges.ends[i+1]) {
				return m, chunk{}, false, ErrCorrupt
			}
		}
	}
	if err := n.checkLayout(); err != nil {
		return m, chunk{}, false, err
	}
	return m, latest, unclean, nil
}

func (n *namespace) writeManifest(m manifest) error {
	raw, err := m.encode()
	if err != nil {
		return err
	}
	return n.publish(".manifest.stage", "manifest.json", raw)
}
