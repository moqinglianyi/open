package rawpreview

import (
	"encoding/binary"
	"io"
)

// TIFF/EXIF field type codes and their element sizes.
const (
	tByte      = 1
	tASCII     = 2
	tShort     = 3
	tLong      = 4
	tSByte     = 6
	tUndefined = 7
	tSShort    = 8
	tSLong     = 9
	tIFD       = 13
	tLong8     = 16
	tSLong8    = 17
	tIFD8      = 18
)

var tiffTypeSize = [...]int64{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8, 4, 0, 0, 8, 8, 8}

func typeSize(t uint16) int64 {
	if int(t) < len(tiffTypeSize) {
		return tiffTypeSize[t]
	}
	return 0
}

// tiffHeader parses the 8 byte TIFF header, returning the byte order and the
// offset of IFD0. RAW containers use vendor magic numbers in place of TIFF's
// 42 (ORF uses "RO"/"RS", RW2 uses 85), so the magic is not checked; a bad
// guess is rejected later by the IFD structure checks.
func tiffHeader(b []byte) (binary.ByteOrder, int64, bool) {
	if len(b) < 8 {
		return nil, 0, false
	}
	var bo binary.ByteOrder
	switch {
	case b[0] == 'I' && b[1] == 'I':
		bo = binary.LittleEndian
	case b[0] == 'M' && b[1] == 'M':
		bo = binary.BigEndian
	default:
		return nil, 0, false
	}
	off := int64(bo.Uint32(b[4:8]))
	if off < 8 {
		return nil, 0, false
	}
	return bo, off, true
}

type ifdEntry struct {
	tag    uint16
	typ    uint16
	count  uint64
	valOff int64
	inline []byte
}

type ifd struct {
	bo      binary.ByteOrder
	entries []ifdEntry
}

const maxIFDEntries = 512

// readIFD reads one image file directory at off, returning it together with the
// offset of the next directory in the chain (0 when there is none).
func readIFD(ra io.ReaderAt, size int64, bo binary.ByteOrder, off int64) (*ifd, int64, bool) {
	if off < 2 || off+2 > size {
		return nil, 0, false
	}
	b, err := slice(ra, size, off, 2)
	if err != nil || len(b) < 2 {
		return nil, 0, false
	}
	n := int64(bo.Uint16(b))
	if n == 0 || n > maxIFDEntries {
		return nil, 0, false
	}
	raw, err := slice(ra, size, off+2, n*12+4)
	if err != nil || int64(len(raw)) < n*12 {
		return nil, 0, false
	}
	f := &ifd{bo: bo, entries: make([]ifdEntry, 0, n)}
	for i := int64(0); i < n; i++ {
		e := raw[i*12 : i*12+12]
		ent := ifdEntry{
			tag:   bo.Uint16(e[0:2]),
			typ:   bo.Uint16(e[2:4]),
			count: uint64(bo.Uint32(e[4:8])),
		}
		if ts := typeSize(ent.typ); ts > 0 && ts*int64(ent.count) <= 4 {
			ent.inline = e[8:12]
		} else {
			ent.valOff = int64(bo.Uint32(e[8:12]))
		}
		f.entries = append(f.entries, ent)
	}
	var next int64
	if int64(len(raw)) >= n*12+4 {
		next = int64(bo.Uint32(raw[n*12 : n*12+4]))
	}
	return f, next, true
}

func (f *ifd) entry(tag uint16) *ifdEntry {
	for i := range f.entries {
		if f.entries[i].tag == tag {
			return &f.entries[i]
		}
	}
	return nil
}

func (f *ifd) decode(b []byte, typ uint16) (uint64, bool) {
	switch typ {
	case tByte, tASCII, tUndefined, tSByte:
		if len(b) >= 1 {
			return uint64(b[0]), true
		}
	case tShort, tSShort:
		if len(b) >= 2 {
			return uint64(f.bo.Uint16(b)), true
		}
	case tLong, tSLong, tIFD:
		if len(b) >= 4 {
			return uint64(f.bo.Uint32(b)), true
		}
	case tLong8, tSLong8, tIFD8:
		if len(b) >= 8 {
			return f.bo.Uint64(b), true
		}
	}
	return 0, false
}

// uint returns the first value of tag when it is stored inline in the entry.
func (f *ifd) uint(tag uint16) (uint64, bool) {
	e := f.entry(tag)
	if e == nil || len(e.inline) == 0 {
		return 0, false
	}
	return f.decode(e.inline, e.typ)
}

// uints reads up to maxN values of tag, following the value offset when the
// values do not fit inside the entry.
func (f *ifd) uints(ra io.ReaderAt, size int64, tag uint16, maxN int) []uint64 {
	e := f.entry(tag)
	if e == nil || e.count == 0 {
		return nil
	}
	ts := typeSize(e.typ)
	if ts == 0 {
		return nil
	}
	n := int64(min(e.count, uint64(maxN)))
	b := e.inline
	if len(b) == 0 {
		var err error
		if b, err = slice(ra, size, e.valOff, n*ts); err != nil {
			return nil
		}
	}
	out := make([]uint64, 0, n)
	for i := int64(0); i < n && (i+1)*ts <= int64(len(b)); i++ {
		if v, ok := f.decode(b[i*ts:], e.typ); ok {
			out = append(out, v)
		}
	}
	return out
}
