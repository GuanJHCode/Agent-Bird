package diagnostics

import (
	"bytes"
	"strings"
	"testing"
)

func testScope() Scope {
	return Scope{Version: 1, RunID: "run", TaskID: "task", AttemptID: "attempt", ProducerID: "producer", PolicySHA256: strings.Repeat("a", 64)}
}

func TestScopeHashBindsEveryDiagnosticAuthority(t *testing.T) {
	base := testScope()
	hash, err := base.Hash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("base hash=%q err=%v", hash, err)
	}
	for _, change := range []func(*Scope){
		func(s *Scope) { s.RunID = "other-run" },
		func(s *Scope) { s.TaskID = "other-task" },
		func(s *Scope) { s.AttemptID = "other-attempt" },
		func(s *Scope) { s.ProducerID = "other-producer" },
		func(s *Scope) { s.PolicySHA256 = strings.Repeat("b", 64) },
	} {
		candidate := base
		change(&candidate)
		got, gotErr := candidate.Hash()
		if gotErr != nil || got == hash {
			t.Fatalf("scope authority not bound: hash=%q err=%v", got, gotErr)
		}
	}
	bad := base
	bad.PolicySHA256 = strings.Repeat("A", 64)
	if _, err = bad.Hash(); err != ErrInvalidScope {
		t.Fatalf("uppercase policy err=%v", err)
	}
}

func TestChunkRoundTripPreservesOffsetsLossAndBytes(t *testing.T) {
	scope := testScope()
	hash, err := scope.Hash()
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		{Stream: Stdout, SegmentID: "segment", Offset: 7, Data: []byte("stdout")},
		{Stream: Stderr, SegmentID: "segment", Offset: 9, LostBefore: 9, Data: []byte{0, 1, 2}},
	}
	raw, err := encodeChunk(hash, 42, "segment", records)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > ChunkBytes {
		t.Fatalf("oversized encoded chunk: %d", len(raw))
	}
	got, err := decodeChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	actual := collectRecords(t, got)
	if got.scopeHash != hash || got.sequence != 42 || got.segmentID != "segment" || len(actual) != len(records) {
		t.Fatalf("header=%+v", got)
	}
	for i := range records {
		if actual[i].Stream != records[i].Stream || actual[i].SegmentID != records[i].SegmentID || actual[i].Offset != records[i].Offset || actual[i].LostBefore != records[i].LostBefore || !bytes.Equal(actual[i].Data, records[i].Data) {
			t.Fatalf("record %d got=%+v want=%+v", i, actual[i], records[i])
		}
	}
}

func TestChunkRejectsOversizeAndCorruptData(t *testing.T) {
	scopeHash, err := testScope().Hash()
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := []Record{{Stream: Stdout, SegmentID: "segment", Data: bytes.Repeat([]byte("x"), RecordBytes+1)}}
	if _, err = encodeChunk(scopeHash, 1, "segment", tooLarge); err != ErrRecordTooLarge {
		t.Fatalf("oversized record err=%v", err)
	}
	raw, err := encodeChunk(scopeHash, 1, "segment", []Record{{Stream: Stdout, SegmentID: "segment", Data: []byte("ok")}})
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if _, err = decodeChunk(raw); err != ErrCorrupt {
		t.Fatalf("corrupt chunk err=%v", err)
	}
}

func TestDecodedRecordDataBorrowsChunkBuffer(t *testing.T) {
	hash, err := testScope().Hash()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChunk(hash, 1, "segment", []Record{{Stream: Stdout, SegmentID: "segment", Data: []byte("payload")}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeChunk(raw)
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(raw, []byte("payload"))
	if at < 0 {
		t.Fatal("payload not in raw chunk")
	}
	raw[at] = 'P'
	if string(collectRecords(t, decoded)[0].Data) != "Payload" {
		t.Fatalf("decoded payload was copied")
	}
}

func collectRecords(t *testing.T, value chunk) []Record {
	t.Helper()
	it := value.Iterator()
	var out []Record
	for {
		r, ok, e := it.Next()
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			return out
		}
		out = append(out, r)
	}
}
