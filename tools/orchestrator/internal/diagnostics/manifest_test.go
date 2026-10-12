//go:build darwin || linux

package diagnostics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func storageScope() Scope {
	return Scope{Version: 1, RunID: "run", TaskID: "task", AttemptID: "attempt", ProducerID: "producer", PolicySHA256: strings.Repeat("1", 64)}
}

func testDescriptor(sequence uint64, raw []byte) descriptor {
	h := sha256.Sum256(raw)
	return descriptor{Sequence: sequence, Size: len(raw), SHA256: hex.EncodeToString(h[:])}
}

func TestManifestCanonicalAndBounded(t *testing.T) {
	s := storageScope()
	m := manifest{Version: 1, Scope: s, Generation: 1}
	raw, err := m.encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeManifest(raw, s)
	if err != nil || got.Generation != 1 {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append([]byte(" "), raw...), bytes.Replace(raw, []byte(`"v":1`), []byte(`"v":1,"v":1`), 1), append(raw, []byte("\n")...), bytes.Repeat([]byte("x"), ManifestBytes+1)} {
		if _, err := decodeManifest(bad, s); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("noncanonical accepted: %v", err)
		}
	}
	other := s
	other.AttemptID = "other"
	if _, err := decodeManifest(raw, other); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("wrong scope %v", err)
	}
	m.Generation = 0
	if _, err := m.encode(); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestManifestWindowAndPendingValidation(t *testing.T) {
	m := manifest{Version: 1, Scope: storageScope(), Generation: 8}
	m.State.SegmentID = "segment"
	m.SegmentFence = "segment"
	m.State.Streams[0] = streamPosition{Seen: true, End: 1}
	for seq := uint64(2); seq <= SlotCount+1; seq++ {
		m.Slots[(seq-1)%SlotCount] = testDescriptor(seq, []byte("x"))
	}
	if _, err := m.encode(); err != nil {
		t.Fatal(err)
	}
	copy := m
	copy.Slots[2] = descriptor{}
	if _, err := copy.encode(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("window gap accepted %v", err)
	}
	copy = m
	copy.Slots[0].Sequence++
	if _, err := copy.encode(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong slot accepted %v", err)
	}
	next := uint64(SlotCount + 2)
	index := int((next - 1) % SlotCount)
	m.Pending = &transition{Index: index, Old: m.Slots[index], New: testDescriptor(next, []byte("new")), State: m.State}
	if _, err := m.encode(); err != nil {
		t.Fatal(err)
	}
	m.Pending.Old.SHA256 = strings.Repeat("2", 64)
	if _, err := m.encode(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("old descriptor mismatch accepted %v", err)
	}
}

func TestManifestRejectsFalseRecoveryAndFenceClaims(t *testing.T) {
	m := manifest{Version: 1, Scope: storageScope(), Generation: 1, SegmentFence: "b"}
	if _, err := m.encode(); err != nil {
		t.Fatalf("reserved empty state invalid %v", err)
	}
	m.State = segmentState{SegmentID: "a"}
	m.State.Streams[0] = streamPosition{Seen: true, End: 3}
	m.Slots[0] = testDescriptor(1, []byte("body"))
	for _, p := range []streamPosition{{Seen: true, End: 3, RecoveryFloor: 4}, {Seen: true, End: 3, RecoveryRequired: true, RecoveryFloor: 2}} {
		bad := m
		bad.State.Streams[0] = p
		if _, err := bad.encode(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("false floor accepted %+v %v", p, err)
		}
	}
	m.SegmentFence = "A"
	if _, err := m.encode(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("active above fence accepted %v", err)
	}
}
