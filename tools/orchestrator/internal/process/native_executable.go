package process

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
)

// NativeExecutableFile checks the existing strict native executable format and entry.
// It does not authenticate the descriptor, pin, directory or execution authority.
func NativeExecutableFile(file *os.File, size int64) bool {
	var head [64]byte
	n, e := file.ReadAt(head[:], 0)
	if e != nil && e != io.EOF || n < 32 || bytes.HasPrefix(head[:n], []byte("#!")) {
		return false
	}
	magic := binary.BigEndian.Uint32(head[:4])
	if magic == 0x7f454c46 {
		if n < 64 || head[6] != 1 || head[4] != 2 || head[5] != 1 && head[5] != 2 {
			return false
		}
		var order binary.ByteOrder = binary.LittleEndian
		if head[5] == 2 {
			order = binary.BigEndian
		}
		typ := order.Uint16(head[16:18])
		entry := order.Uint64(head[24:32])
		off := order.Uint64(head[32:40])
		entrySize := order.Uint16(head[54:56])
		count := order.Uint16(head[56:58])
		if (typ != 2 && typ != 3) || entry == 0 || entrySize != 56 || count < 1 || count > 256 || off > uint64(size) || uint64(count)*56 > uint64(size)-off {
			return false
		}
		var ph [56]byte
		executable := false
		for i := uint16(0); i < count; i++ {
			if _, e = file.ReadAt(ph[:], int64(off)+int64(i)*56); e != nil {
				return false
			}
			if order.Uint32(ph[:4]) == 1 {
				at := order.Uint64(ph[8:16])
				vaddr := order.Uint64(ph[16:24])
				length := order.Uint64(ph[32:40])
				mem := order.Uint64(ph[40:48])
				if at > uint64(size) || length > uint64(size)-at || length > mem {
					return false
				}
				executable = executable || (order.Uint32(ph[4:8])&1 != 0 && entry >= vaddr && entry-vaddr < mem)
			}
		}
		return executable
	}
	if magic == 0xcafebabe || magic == 0xbebafeca {
		var order binary.ByteOrder = binary.BigEndian
		if magic == 0xbebafeca {
			order = binary.LittleEndian
		}
		count := order.Uint32(head[4:8])
		if count < 1 || count > 32 || int64(8+count*20) > size {
			return false
		}
		var arch [20]byte
		for i := uint32(0); i < count; i++ {
			if _, e = file.ReadAt(arch[:], int64(8+i*20)); e != nil {
				return false
			}
			at := order.Uint32(arch[8:12])
			length := order.Uint32(arch[12:16])
			if length < 32 || int64(at)+int64(length) > size || !nativeMachOHeader(file, int64(at), int64(length)) {
				return false
			}
		}
		return true
	}
	return nativeMachOHeader(file, 0, size)
}
func nativeMachOHeader(file *os.File, at, size int64) bool {
	var h [32]byte
	if _, e := file.ReadAt(h[:], at); e != nil {
		return false
	}
	magic := binary.BigEndian.Uint32(h[:4])
	var order binary.ByteOrder = binary.BigEndian
	headerSize := int64(28)
	switch magic {
	case 0xfeedface:
	case 0xcefaedfe:
		order = binary.LittleEndian
	case 0xfeedfacf:
		headerSize = 32
	case 0xcffaedfe:
		headerSize = 32
		order = binary.LittleEndian
	default:
		return false
	}
	count := order.Uint32(h[16:20])
	commands := order.Uint32(h[20:24])
	if order.Uint32(h[12:16]) != 2 || count < 1 || count > 256 || commands > 1<<20 || int64(commands) > size-headerSize {
		return false
	}
	offset := headerSize
	entry := false
	var command [8]byte
	for i := uint32(0); i < count; i++ {
		if _, e := file.ReadAt(command[:], at+offset); e != nil {
			return false
		}
		kind := order.Uint32(command[:4])
		length := order.Uint32(command[4:8])
		if length < 8 || int64(length) > headerSize+int64(commands)-offset {
			return false
		}
		entry = entry || kind == 0x80000028 || kind == 0x5
		offset += int64(length)
	}
	return entry && offset == headerSize+int64(commands)
}
