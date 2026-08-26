package rawpreview

import (
	"encoding/binary"
	"encoding/hex"
	"io"
)

// addProbed appends a candidate whose length is unknown, determining it by
// walking the JPEG to its EOI marker.
func addProbed(ra io.ReaderAt, size, off, maxLen int64, source string, out *[]Candidate) bool {
	info, ok := probeJPEG(ra, size, off, min(maxLen, MaxParseBytes))
	if !ok || info.Length <= 0 {
		return false
	}
	return addCandidate(ra, size, off, info.Length, info.Orientation, source, out)
}

// ---------------------------------------------------------------- Fujifilm RAF

// rafCandidates reads the fixed RAF header, which points straight at a
// full resolution JPEG rendition of the frame.
func rafCandidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	head, err := slice(ra, size, 0, 0x60)
	if err != nil || len(head) < 0x60 || string(head[0:16]) != "FUJIFILMCCD-RAW " {
		return false
	}
	off := int64(binary.BigEndian.Uint32(head[0x54:0x58]))
	length := int64(binary.BigEndian.Uint32(head[0x58:0x5c]))
	return addCandidate(ra, size, off, length, 0, "raf", out)
}

// ------------------------------------------------------------------ Canon CR3

type bmffBox struct {
	typ    string
	uuid   string
	off    int64 // payload start, past the usertype of a uuid box
	length int64
}

var bmffContainers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "uuid": true, "udta": true, "edts": true, "dinf": true,
}

// walkBMFF iterates the ISO base media boxes in [off, end), calling fn for each
// one. fn reports whether the box should be descended into.
func walkBMFF(ra io.ReaderAt, size, off, end int64, depth int, fn func(bmffBox, int) bool) {
	if depth > 6 {
		return
	}
	for off+8 <= end {
		h, err := slice(ra, size, off, 16)
		if err != nil || len(h) < 8 {
			return
		}
		boxLen := int64(binary.BigEndian.Uint32(h[0:4]))
		typ := string(h[4:8])
		hdr := int64(8)
		switch boxLen {
		case 1:
			if len(h) < 16 {
				return
			}
			boxLen = int64(binary.BigEndian.Uint64(h[8:16]))
			hdr = 16
		case 0:
			boxLen = end - off
		}
		if boxLen < hdr || off+boxLen > end {
			boxLen = end - off // tolerate a truncated trailing box
			if boxLen < hdr {
				return
			}
		}
		b := bmffBox{typ: typ, off: off + hdr, length: boxLen - hdr}
		if typ == "uuid" && b.length >= 16 {
			if u, err := slice(ra, size, b.off, 16); err == nil && len(u) == 16 {
				b.uuid = hex.EncodeToString(u)
				b.off += 16
				b.length -= 16
			}
		}
		if fn(b, depth) {
			walkBMFF(ra, size, b.off, b.off+b.length, depth+1, fn)
		}
		off += boxLen
	}
}

// cr3Candidates finds the renditions Canon's CR3 container carries: a THMB
// thumbnail and a PRVW preview inside vendor uuid boxes, plus the full size
// JPEG track that occupies the front of mdat.
func cr3Candidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	found := false
	walkBMFF(ra, size, 0, size, 0, func(b bmffBox, depth int) bool {
		switch b.typ {
		case "THMB", "PRVW":
			// A short vendor header precedes the JPEG; find SOI rather than
			// depending on its exact layout, which varies between models.
			if h, err := slice(ra, size, b.off, min(b.length, 64)); err == nil {
				if i := findSOI(h); i >= 0 {
					if addProbed(ra, size, b.off+int64(i), b.length-int64(i), "cr3:"+b.typ, out) {
						found = true
					}
				}
			}
			return false
		case "mdat":
			if h, err := slice(ra, size, b.off, min(b.length, 4<<10)); err == nil {
				if i := findSOI(h); i >= 0 {
					if addProbed(ra, size, b.off+int64(i), b.length-int64(i), "cr3:mdat", out) {
						found = true
					}
				}
			}
			return false
		}
		return bmffContainers[b.typ]
	})
	return found
}

// ------------------------------------------------------------------ Canon CRW

// crwCandidates walks the CIFF directory, which lives at the end of the file
// and holds the JPEG thumbnail under tag 0x2007.
func crwCandidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	head, err := slice(ra, size, 0, 26)
	if err != nil || len(head) < 26 || string(head[6:14]) != "HEAPCCDR" {
		return false
	}
	var bo binary.ByteOrder
	switch string(head[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return false
	}
	base := int64(bo.Uint32(head[2:6]))
	if base < 26 || base >= size {
		return false
	}
	tail, err := slice(ra, size, size-4, 4)
	if err != nil || len(tail) < 4 {
		return false
	}
	return crwDir(ra, size, bo, base, base+int64(bo.Uint32(tail)), 0, out)
}

func crwDir(ra io.ReaderAt, size int64, bo binary.ByteOrder, base, off int64, depth int, out *[]Candidate) bool {
	if depth > 3 || off < 0 || off+2 > size {
		return false
	}
	b, err := slice(ra, size, off, 2)
	if err != nil || len(b) < 2 {
		return false
	}
	n := int64(bo.Uint16(b))
	if n == 0 || n > 512 {
		return false
	}
	raw, err := slice(ra, size, off+2, n*10)
	if err != nil || int64(len(raw)) < n*10 {
		return false
	}
	found := false
	for i := int64(0); i < n; i++ {
		e := raw[i*10 : i*10+10]
		tag := bo.Uint16(e[0:2])
		if tag&0xc000 != 0 { // value stored inside the entry
			continue
		}
		length := int64(bo.Uint32(e[2:6]))
		doff := base + int64(bo.Uint32(e[6:10]))
		if dt := (tag >> 11) & 7; dt == 5 || dt == 6 { // nested directory
			if crwDir(ra, size, bo, base, doff, depth+1, out) {
				found = true
			}
			continue
		}
		if addCandidate(ra, size, doff, length, 0, "crw", out) {
			found = true
		}
	}
	return found
}

// -------------------------------------------------------------- Minolta MRW

// mrwCandidates locates the TTW block, which embeds a complete TIFF/EXIF
// structure, and reuses the TIFF walker on it.
func mrwCandidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	head, err := slice(ra, size, 0, 8)
	if err != nil || len(head) < 8 || string(head[0:4]) != "\x00MRM" {
		return false
	}
	end := min(8+int64(binary.BigEndian.Uint32(head[4:8])), size)
	for off := int64(8); off+8 <= end; {
		h, err := slice(ra, size, off, 8)
		if err != nil || len(h) < 8 {
			return false
		}
		blockLen := int64(binary.BigEndian.Uint32(h[4:8]))
		if blockLen <= 0 {
			return false
		}
		if string(h[0:4]) == "\x00TTW" {
			var inner []Candidate
			if tiffCandidates(io.NewSectionReader(ra, off+8, blockLen), blockLen, "mrw", &inner) {
				for _, c := range inner {
					c.Offset += off + 8
					*out = append(*out, c)
				}
				return true
			}
		}
		off += 8 + blockLen
	}
	return false
}

// -------------------------------------------------------------- Sigma X3F

// x3fCandidates reads the trailing SECd directory and probes its image sections.
func x3fCandidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	head, err := slice(ra, size, 0, 4)
	if err != nil || string(head) != "FOVb" {
		return false
	}
	tail, err := slice(ra, size, size-4, 4)
	if err != nil || len(tail) < 4 {
		return false
	}
	dir := int64(binary.LittleEndian.Uint32(tail))
	d, err := slice(ra, size, dir, 12)
	if err != nil || len(d) < 12 || string(d[0:4]) != "SECd" {
		return false
	}
	n := int64(binary.LittleEndian.Uint32(d[8:12]))
	if n == 0 || n > 256 {
		return false
	}
	raw, err := slice(ra, size, dir+12, n*12)
	if err != nil || int64(len(raw)) < n*12 {
		return false
	}
	found := false
	for i := int64(0); i < n; i++ {
		e := raw[i*12 : i*12+12]
		off := int64(binary.LittleEndian.Uint32(e[0:4]))
		length := int64(binary.LittleEndian.Uint32(e[4:8]))
		if length < 256 {
			continue
		}
		h, err := slice(ra, size, off, min(length, 128))
		if err != nil {
			continue
		}
		if j := findSOI(h); j >= 0 {
			if addCandidate(ra, size, off+int64(j), length-int64(j), 0, "x3f", out) {
				found = true
			}
		}
	}
	return found
}
