package diagnostics

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"unicode/utf8"
)

var (
	ErrInvalidScope   = errors.New("diagnostics_invalid_scope")
	ErrRecordTooLarge = errors.New("diagnostics_record_too_large")
	ErrChunkFull      = errors.New("diagnostics_chunk_full")
	ErrCorrupt        = errors.New("diagnostics_corrupt")
)

// Scope names the one authority a detailed attempt log may represent. The
// hash is canonical binary framing, rather than delimiter concatenation.
type Scope struct {
	Version      uint16
	RunID        string
	TaskID       string
	AttemptID    string
	ProducerID   string
	PolicySHA256 string
}

func (s Scope) Hash() (string, error) {
	if s.Version != 1 || !validScopeID(s.RunID) || !validScopeID(s.TaskID) || !validScopeID(s.AttemptID) || !validScopeID(s.ProducerID) || !validPolicyHash(s.PolicySHA256) {
		return "", ErrInvalidScope
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, s.Version)
	for _, value := range []string{s.RunID, s.TaskID, s.AttemptID, s.ProducerID, s.PolicySHA256} {
		_ = binary.Write(&b, binary.BigEndian, uint16(len(value)))
		_, _ = b.WriteString(value)
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func validScopeID(value string) bool {
	return len(value) > 0 && len(value) <= 256 && utf8.ValidString(value) && !bytes.ContainsRune([]byte(value), 0)
}

func validPolicyHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

const (
	chunkVersion = uint16(1)
	chunkMagic   = "DGCHNK01"
	chunkTrailer = sha256.Size
)

type chunk struct {
	scopeHash string
	sequence  uint64
	segmentID string
	raw       []byte
	recordAt  int
	count     int
}

type chunkIterator struct {
	chunk     chunk
	at, index int
	ends      [3]uint64
}

func (c chunk) Iterator() chunkIterator { return chunkIterator{chunk: c, at: c.recordAt} }
func (i *chunkIterator) Next() (Record, bool, error) {
	if i.index == i.chunk.count {
		return Record{}, false, nil
	}
	b := i.chunk.raw
	if i.at+19 > len(b)-chunkTrailer {
		return Record{}, false, ErrCorrupt
	}
	s := Stream(b[i.at])
	off := binary.BigEndian.Uint64(b[i.at+1:])
	lost := binary.BigEndian.Uint64(b[i.at+9:])
	n := int(binary.BigEndian.Uint16(b[i.at+17:]))
	i.at += 19
	if (s != Stdout && s != Stderr) || n > RecordBytes || i.at+n > len(b)-chunkTrailer || lost > off || (n == 0 && lost == 0) || uint64(n) > ^uint64(0)-off {
		return Record{}, false, ErrCorrupt
	}
	gapStart := off - lost
	if i.ends[s] != 0 && gapStart != i.ends[s] {
		return Record{}, false, ErrCorrupt
	}
	i.ends[s] = off + uint64(n)
	data := b[i.at : i.at+n]
	i.at += n
	i.index++
	return Record{Stream: s, SegmentID: i.chunk.segmentID, Offset: off, LostBefore: lost, Data: data}, true, nil
}

func encodeChunk(scopeHash string, sequence uint64, segmentID string, records []Record) ([]byte, error) {
	if len(segmentID) == 0 || len(segmentID) > 128 || !utf8.ValidString(segmentID) || len(records) > 65535 {
		return nil, ErrCorrupt
	}
	scope, err := hex.DecodeString(scopeHash)
	if err != nil || len(scope) != sha256.Size {
		return nil, ErrInvalidScope
	}
	total := len(chunkMagic) + 2 + sha256.Size + 8 + 1 + len(segmentID) + 2 + chunkTrailer
	for _, record := range records {
		if (record.Stream != Stdout && record.Stream != Stderr) || record.SegmentID != segmentID || len(record.Data) > RecordBytes {
			if len(record.Data) > RecordBytes {
				return nil, ErrRecordTooLarge
			}
			return nil, ErrCorrupt
		}
		total += 1 + 8 + 8 + 2 + len(record.Data)
		if total > ChunkBytes {
			return nil, ErrChunkFull
		}
	}
	var b bytes.Buffer
	b.Grow(total)
	b.WriteString(chunkMagic)
	_ = binary.Write(&b, binary.BigEndian, chunkVersion)
	b.Write(scope)
	_ = binary.Write(&b, binary.BigEndian, sequence)
	b.WriteByte(byte(len(segmentID)))
	b.WriteString(segmentID)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(records)))
	for _, record := range records {
		b.WriteByte(byte(record.Stream))
		_ = binary.Write(&b, binary.BigEndian, record.Offset)
		_ = binary.Write(&b, binary.BigEndian, record.LostBefore)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(record.Data)))
		b.Write(record.Data)
	}
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes(), nil
}

func decodeChunk(raw []byte) (chunk, error) {
	if len(raw) < len(chunkMagic)+2+sha256.Size+8+1+2+chunkTrailer || len(raw) > ChunkBytes {
		return chunk{}, ErrCorrupt
	}
	body, trailer := raw[:len(raw)-chunkTrailer], raw[len(raw)-chunkTrailer:]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], trailer) || string(body[:len(chunkMagic)]) != chunkMagic {
		return chunk{}, ErrCorrupt
	}
	offset := len(chunkMagic)
	version := binary.BigEndian.Uint16(body[offset:])
	offset += 2
	if version != chunkVersion {
		return chunk{}, ErrCorrupt
	}
	scopeHash := hex.EncodeToString(body[offset : offset+sha256.Size])
	offset += sha256.Size
	sequence := binary.BigEndian.Uint64(body[offset:])
	offset += 8
	segmentLength := int(body[offset])
	offset++
	if segmentLength == 0 || segmentLength > 128 || offset+segmentLength+2 > len(body) {
		return chunk{}, ErrCorrupt
	}
	segmentID := string(body[offset : offset+segmentLength])
	offset += segmentLength
	if !utf8.ValidString(segmentID) {
		return chunk{}, ErrCorrupt
	}
	count := int(binary.BigEndian.Uint16(body[offset:]))
	offset += 2
	if count > (len(body)-offset)/(1+8+8+2) {
		return chunk{}, ErrCorrupt
	}
	recordAt := offset
	view := chunk{segmentID: segmentID, raw: raw, recordAt: recordAt, count: count}
	it := view.Iterator()
	for range count {
		if _, ok, err := it.Next(); err != nil || !ok {
			return chunk{}, ErrCorrupt
		}
	}
	if it.at != len(body) {
		return chunk{}, ErrCorrupt
	}
	view.scopeHash, view.sequence = scopeHash, sequence
	return view, nil
}
