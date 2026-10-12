//go:build darwin || linux

package diagnostics

import (
	"bytes"
	"encoding/json"
	"io"
)

type descriptor struct {
	Sequence uint64 `json:"q"`
	Size     int    `json:"n"`
	SHA256   string `json:"h"`
}

type streamPosition struct {
	Seen             bool   `json:"s"`
	End              uint64 `json:"e"`
	RecoveryRequired bool   `json:"r"`
	RecoveryFloor    uint64 `json:"f"`
}

type segmentState struct {
	SegmentID string            `json:"id"`
	Streams   [2]streamPosition `json:"streams"`
}

type transition struct {
	Index int          `json:"i"`
	Old   descriptor   `json:"old"`
	New   descriptor   `json:"new"`
	State segmentState `json:"state"`
}

type manifest struct {
	Version      int                   `json:"v"`
	Scope        Scope                 `json:"scope"`
	Generation   uint64                `json:"g"`
	Closed       bool                  `json:"closed"`
	Incomplete   bool                  `json:"incomplete"`
	SegmentFence string                `json:"segment_fence"`
	State        segmentState          `json:"state"`
	Slots        [SlotCount]descriptor `json:"slots"`
	Pending      *transition           `json:"pending,omitempty"`
}

func (s segmentState) valid() bool {
	if s.SegmentID == "" {
		return s.Streams == [2]streamPosition{}
	}
	if !validSegmentID(s.SegmentID) {
		return false
	}
	for _, p := range s.Streams {
		if !p.Seen && p.End != 0 {
			return false
		}
		if (!p.RecoveryRequired && p.RecoveryFloor != 0) || (p.RecoveryRequired && p.RecoveryFloor < p.End) {
			return false
		}
	}
	return true
}

func (d descriptor) valid() bool {
	if d.Sequence == 0 {
		return d == descriptor{}
	}
	return d.Size > 0 && d.Size <= ChunkBytes && validPolicyHash(d.SHA256)
}

func (m manifest) window() (first, last uint64, count int) {
	for _, d := range m.Slots {
		if d.Sequence == 0 {
			continue
		}
		count++
		if first == 0 || d.Sequence < first {
			first = d.Sequence
		}
		if d.Sequence > last {
			last = d.Sequence
		}
	}
	return
}

func (m manifest) validate() error {
	if m.Version != 1 || m.Generation == 0 || !m.State.valid() {
		return ErrCorrupt
	}
	if _, err := m.Scope.Hash(); err != nil {
		return err
	}
	if (m.SegmentFence != "" && !validSegmentID(m.SegmentFence)) || m.State.SegmentID > m.SegmentFence {
		return ErrCorrupt
	}
	for i, d := range m.Slots {
		if !d.valid() || (d.Sequence > 0 && int((d.Sequence-1)%SlotCount) != i) {
			return ErrCorrupt
		}
	}
	first, last, count := m.window()
	if count > 0 {
		if last-first != uint64(count-1) || m.State.SegmentID == "" {
			return ErrCorrupt
		}
	} else if m.State != (segmentState{}) {
		return ErrCorrupt
	}
	if p := m.Pending; p != nil {
		if m.Closed || p.Index < 0 || p.Index >= SlotCount || !p.New.valid() || p.New.Sequence == 0 || p.Old != m.Slots[p.Index] || !p.State.valid() || p.State.SegmentID == "" {
			return ErrCorrupt
		}
		if int((p.New.Sequence-1)%SlotCount) != p.Index {
			return ErrCorrupt
		}
		if p.State.SegmentID > m.SegmentFence {
			return ErrCorrupt
		}
		if p.State.SegmentID != m.State.SegmentID && (p.State.SegmentID != m.SegmentFence || p.State.SegmentID <= m.State.SegmentID) {
			return ErrCorrupt
		}
		if p.New.Sequence == last && last != 0 {
			if p.New.Size <= p.Old.Size || p.State.SegmentID != m.State.SegmentID {
				return ErrCorrupt
			}
		} else if last == ^uint64(0) || p.New.Sequence != last+1 {
			return ErrCorrupt
		}
	}
	return nil
}

func (m manifest) encode() ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > ManifestBytes {
		return nil, ErrCorrupt
	}
	return raw, nil
}

func decodeManifest(raw []byte, scope Scope) (manifest, error) {
	var m manifest
	if len(raw) == 0 || len(raw) > ManifestBytes {
		return m, ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return manifest{}, ErrCorrupt
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return manifest{}, ErrCorrupt
	}
	canonical, err := m.encode()
	if err != nil || !bytes.Equal(canonical, raw) {
		return manifest{}, ErrCorrupt
	}
	if m.Scope != scope {
		return manifest{}, ErrInvalidScope
	}
	return m, nil
}

func (m *manifest) markRecovery() {
	m.Incomplete = true
	if m.State.SegmentID != "" {
		for i := range m.State.Streams {
			m.State.Streams[i].RecoveryRequired = true
			m.State.Streams[i].RecoveryFloor = max(m.State.Streams[i].RecoveryFloor, m.State.Streams[i].End)
		}
	}
}

func (m *manifest) applyPending(newTarget bool) {
	if newTarget {
		m.Slots[m.Pending.Index] = m.Pending.New
		m.State = m.Pending.State
	} else if m.Pending.State.SegmentID == m.State.SegmentID {
		for i := range m.State.Streams {
			p := &m.State.Streams[i]
			floor := max(p.RecoveryFloor, m.Pending.State.Streams[i].End, m.Pending.State.Streams[i].RecoveryFloor)
			if floor > p.End || p.RecoveryRequired {
				p.RecoveryRequired = true
				p.RecoveryFloor = floor
			}
		}
	}
	m.Pending = nil
}
