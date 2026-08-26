package rawpreview

import (
	"bytes"
	"encoding/binary"
	"io"
)

// jpegInfo describes a JPEG stream located inside a container.
type jpegInfo struct {
	Length      int64
	Width       int
	Height      int
	Orientation int
	// Offset/Length of the JPEG stored in the EXIF IFD1 thumbnail, relative to
	// the start of this JPEG. Zero length means absent.
	ThumbOffset int64
	ThumbLength int64
}

func isSOI(b []byte) bool {
	return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
}

// findSOI returns the index of the first SOI marker in b, or -1.
func findSOI(b []byte) int {
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0xFF && b[i+1] == 0xD8 && b[i+2] == 0xFF {
			return i
		}
	}
	return -1
}

func isSOF(marker byte) bool {
	return marker >= 0xC0 && marker <= 0xCF &&
		marker != 0xC4 && marker != 0xC8 && marker != 0xCC
}

// probeJPEG walks the marker structure of the JPEG starting at off, determining
// its exact length by locating EOI. The cost is roughly the size of the JPEG.
func probeJPEG(ra io.ReaderAt, size, off, max int64) (jpegInfo, bool) {
	return probeJPEGAt(ra, size, off, max, true)
}

// probeJPEGHeader reads only the marker segments preceding the entropy coded
// data, which is enough for the dimensions, the orientation and the EXIF
// thumbnail locator. Use it when the length is already known from a container
// field, since it reads a few KiB instead of the whole image.
func probeJPEGHeader(ra io.ReaderAt, size, off int64) (jpegInfo, bool) {
	return probeJPEGAt(ra, size, off, 1<<20, false)
}

func probeJPEGAt(ra io.ReaderAt, size, off, max int64, needLength bool) (jpegInfo, bool) {
	var info jpegInfo
	head, err := slice(ra, size, off, 2)
	if err != nil || len(head) < 2 || head[0] != 0xFF || head[1] != 0xD8 {
		return info, false
	}
	limit := min(max, size-off)
	pos := int64(2)
	for pos+4 <= limit {
		hdr, err := slice(ra, size, off+pos, 4)
		if err != nil || len(hdr) < 4 {
			return info, false
		}
		if hdr[0] != 0xFF {
			return info, false
		}
		marker := hdr[1]
		for marker == 0xFF { // fill bytes
			pos++
			hdr, err = slice(ra, size, off+pos, 4)
			if err != nil || len(hdr) < 4 || hdr[0] != 0xFF {
				return info, false
			}
			marker = hdr[1]
		}
		if marker == 0xD9 { // EOI without image data
			info.Length = pos + 2
			return info, info.Width > 0
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		segLen := int64(binary.BigEndian.Uint16(hdr[2:4]))
		if segLen < 2 {
			return info, false
		}
		switch {
		case isSOF(marker):
			b, err := slice(ra, size, off+pos+4, 5)
			if err != nil || len(b) < 5 {
				return info, false
			}
			info.Height = int(binary.BigEndian.Uint16(b[1:3]))
			info.Width = int(binary.BigEndian.Uint16(b[3:5]))
		case marker == 0xE1: // APP1, possibly EXIF
			n := min(segLen-2, 128<<10)
			if b, err := slice(ra, size, off+pos+4, n); err == nil {
				readExifAPP1(b, pos+4, &info)
			}
		case marker == 0xDA: // SOS: entropy coded data follows
			if !needLength {
				return info, info.Width > 0
			}
			start := pos + 2 + segLen
			end, ok := findEOI(ra, size, off+start, limit-start)
			if !ok {
				return info, false
			}
			info.Length = start + end
			return info, info.Width > 0
		}
		pos += 2 + segLen
	}
	return info, false
}

// findEOI scans raw entropy coded data for the EOI marker. Inside entropy data
// every 0xFF is either stuffed with 0x00 or part of a restart marker, so a
// plain byte search for FF D9 cannot produce a false positive here.
func findEOI(ra io.ReaderAt, size, start, max int64) (int64, bool) {
	const chunk = 64 << 10
	var scanned int64
	var carry bool // previous chunk ended with 0xFF
	for scanned < max {
		b, err := slice(ra, size, start+scanned, min(chunk, max-scanned))
		if err != nil || len(b) == 0 {
			return 0, false
		}
		if carry && b[0] == 0xD9 {
			return scanned + 1, true
		}
		if i := bytes.Index(b, []byte{0xFF, 0xD9}); i >= 0 {
			return scanned + int64(i) + 2, true
		}
		carry = b[len(b)-1] == 0xFF
		scanned += int64(len(b))
	}
	return 0, false
}

// readExifAPP1 pulls the orientation and the IFD1 thumbnail locator out of an
// APP1 payload. payloadOff is the payload position relative to the SOI, so the
// returned thumbnail offset is also relative to the SOI.
func readExifAPP1(b []byte, payloadOff int64, info *jpegInfo) {
	if len(b) < 8 || !bytes.HasPrefix(b, []byte("Exif\x00\x00")) {
		return
	}
	tiff := b[6:]
	base := payloadOff + 6
	bo, ifd0, ok := tiffHeader(tiff)
	if !ok {
		return
	}
	tr := bytes.NewReader(tiff)
	// IFD0 holds Orientation, IFD1 (its "next" pointer) holds the thumbnail.
	e0, next, ok := readIFD(tr, int64(len(tiff)), bo, ifd0)
	if !ok {
		return
	}
	if v, ok := e0.uint(0x0112); ok && v >= 1 && v <= 8 {
		info.Orientation = int(v)
	}
	if next <= 0 {
		return
	}
	e1, _, ok := readIFD(tr, int64(len(tiff)), bo, next)
	if !ok {
		return
	}
	o, ok1 := e1.uint(0x0201)
	l, ok2 := e1.uint(0x0202)
	if ok1 && ok2 && l > 0 {
		info.ThumbOffset = base + int64(o)
		info.ThumbLength = int64(l)
	}
}
