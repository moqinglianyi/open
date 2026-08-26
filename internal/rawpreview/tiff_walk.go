package rawpreview

import "io"

// Tags carrying embedded JPEG locators, plus the ones used to navigate.
const (
	tagNewSubfileType = 0x00fe
	tagStripOffsets   = 0x0111
	tagCompression    = 0x0103
	tagStripByteCount = 0x0117
	tagOrientation    = 0x0112
	tagTileOffsets    = 0x0144
	tagTileByteCounts = 0x0145
	tagSubIFDs        = 0x014a
	tagExifIFD        = 0x8769
	tagJPEGIFOffset   = 0x0201
	tagJPEGIFLength   = 0x0202
	tagJpgFromRaw     = 0x002e // Panasonic RW2/RAW
)

const maxWalkedIFDs = 64

// addCandidate validates a locator and, if it really points at a JPEG, appends
// it (and any thumbnail embedded in its own EXIF) to out.
func addCandidate(ra io.ReaderAt, size, off, length int64, orient int, source string, out *[]Candidate) bool {
	if off <= 0 || length < 256 || off+4 > size {
		return false
	}
	length = min(length, size-off)
	b, err := slice(ra, size, off, 3)
	if err != nil || !isSOI(b) {
		return false
	}
	info, ok := probeJPEGHeader(ra, size, off)
	if !ok {
		return false
	}
	c := Candidate{
		Offset: off, Length: length,
		Width: info.Width, Height: info.Height,
		Orientation: orient, Source: source,
	}
	if info.Orientation != 0 {
		c.Orientation = info.Orientation
	}
	*out = append(*out, c)
	if info.ThumbLength > 0 && info.ThumbOffset > 0 {
		t := Candidate{
			Offset: off + info.ThumbOffset, Length: info.ThumbLength,
			Orientation: c.Orientation, Source: source + "+exifthumb",
		}
		if t.Offset+t.Length <= size {
			if ti, ok := probeJPEGHeader(ra, size, t.Offset); ok {
				t.Width, t.Height = ti.Width, ti.Height
				*out = append(*out, t)
			}
		}
	}
	return true
}

func addPair(ra io.ReaderAt, size int64, f *ifd, offTag, lenTag uint16, orient int, source string, out *[]Candidate) bool {
	offs := f.uints(ra, size, offTag, 1)
	lens := f.uints(ra, size, lenTag, 1)
	if len(offs) == 0 || len(lens) == 0 {
		return false
	}
	return addCandidate(ra, size, int64(offs[0]), int64(lens[0]), orient, source, out)
}

// tiffCandidates walks the IFD tree of a TIFF based RAW container (CR2, NEF,
// NRW, ARW, SR2, DNG, RW2, ORF, PEF, SRW, ERF, 3FR, IIQ, GPR, KDC, MOS ...)
// and collects every embedded JPEG rendition it finds.
func tiffCandidates(ra io.ReaderAt, size int64, source string, out *[]Candidate) bool {
	head, err := slice(ra, size, 0, 8)
	if err != nil {
		return false
	}
	bo, ifd0, ok := tiffHeader(head)
	if !ok {
		return false
	}
	visited := make(map[int64]bool, 8)
	queue := []int64{ifd0}
	found := false
	for len(queue) > 0 && len(visited) < maxWalkedIFDs {
		off := queue[0]
		queue = queue[1:]
		if off <= 0 || off >= size || visited[off] {
			continue
		}
		visited[off] = true
		f, next, ok := readIFD(ra, size, bo, off)
		if !ok {
			continue
		}
		if next > 0 {
			queue = append(queue, next)
		}
		for _, v := range f.uints(ra, size, tagSubIFDs, 16) {
			queue = append(queue, int64(v))
		}
		if v, ok := f.uint(tagExifIFD); ok {
			queue = append(queue, int64(v))
		}

		orient := 0
		if v, ok := f.uint(tagOrientation); ok && v >= 1 && v <= 8 {
			orient = int(v)
		}
		if addPair(ra, size, f, tagJPEGIFOffset, tagJPEGIFLength, orient, source, out) {
			found = true
		}
		// A strip or tile holding a JPEG compressed rendition. Only probed for
		// JPEG compression values, so the (large) raw sensor strips are skipped.
		if c, ok := f.uint(tagCompression); ok && (c == 6 || c == 7 || c == 99) {
			if addPair(ra, size, f, tagStripOffsets, tagStripByteCount, orient, source, out) {
				found = true
			}
			if addPair(ra, size, f, tagTileOffsets, tagTileByteCounts, orient, source, out) {
				found = true
			}
		}
		// Panasonic stores the whole JPEG inline as one oversized field.
		if e := f.entry(tagJpgFromRaw); e != nil && e.count > 1024 && e.valOff > 0 {
			if addCandidate(ra, size, e.valOff, int64(e.count), orient, source+":jpgfromraw", out) {
				found = true
			}
		}
	}
	return found
}
