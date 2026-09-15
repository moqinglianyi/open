package rawpreview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/disintegration/imaging"
)

// ---------------------------------------------------------------- primitives

func writeU16(w io.Writer, bo binary.ByteOrder, v uint16) {
	b := make([]byte, 2)
	bo.PutUint16(b, v)
	_, _ = w.Write(b)
}

func writeU32(w io.Writer, bo binary.ByteOrder, v uint32) {
	b := make([]byte, 4)
	bo.PutUint32(b, v)
	_, _ = w.Write(b)
}

func tiffHeaderBytes(bo binary.ByteOrder, ifd0 int64) []byte {
	b := make([]byte, 8)
	if bo == binary.BigEndian {
		copy(b, "MM")
	} else {
		copy(b, "II")
	}
	bo.PutUint16(b[2:4], 42)
	bo.PutUint32(b[4:8], uint32(ifd0))
	return b
}

// writeIFDEntry writes one 12 byte directory entry. A value small enough to fit
// is stored inside the entry, left justified, the way TIFF requires.
func writeIFDEntry(w io.Writer, bo binary.ByteOrder, tag, typ uint16, count uint32, val uint64) {
	b := make([]byte, 12)
	bo.PutUint16(b[0:2], tag)
	bo.PutUint16(b[2:4], typ)
	bo.PutUint32(b[4:8], count)
	switch ts := typeSize(typ); {
	case ts == 2 && count == 1:
		bo.PutUint16(b[8:10], uint16(val))
	default:
		bo.PutUint32(b[8:12], uint32(val))
	}
	_, _ = w.Write(b)
}

// ---------------------------------------------------------------- JPEG fixture

// buildJPEG assembles a structurally valid baseline JPEG whose entropy coded
// data is filler. pad pushes the stream past the 256 byte floor addCandidate
// enforces; an EXIF APP1 segment is included when an orientation or a thumbnail
// is asked for.
func buildJPEG(w, h, orientation int, thumb []byte, pad int) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	if orientation != 0 || len(thumb) > 0 {
		app1 := buildExifAPP1(binary.LittleEndian, orientation, thumb)
		b.Write([]byte{0xFF, 0xE1})
		writeU16(&b, binary.BigEndian, uint16(len(app1)+2))
		b.Write(app1)
	}
	b.Write([]byte{0xFF, 0xC0, 0x00, 0x11, 0x08}) // SOF0, 3 components
	writeU16(&b, binary.BigEndian, uint16(h))
	writeU16(&b, binary.BigEndian, uint16(w))
	b.Write([]byte{0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01})
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x0C, 0x03, 0x01, 0x00, 0x02, 0x11, 0x03, 0x11, 0x00, 0x3F, 0x00})
	if pad > 0 {
		b.Write(bytes.Repeat([]byte{0x55}, pad))
	}
	b.Write([]byte{0xFF, 0xD9}) // EOI
	return b.Bytes()
}

// buildExifAPP1 builds an EXIF APP1 payload: IFD0 carries the orientation and,
// when a thumbnail is supplied, IFD1 locates it inside the payload itself, which
// is where a camera puts it.
func buildExifAPP1(bo binary.ByteOrder, orientation int, thumb []byte) []byte {
	const ifd0Off = 8
	ifd1Off := int64(ifd0Off + 2 + 12*1 + 4)
	thumbOff := ifd1Off + 2 + 12*2 + 4

	var t bytes.Buffer
	t.Write(tiffHeaderBytes(bo, ifd0Off))
	writeU16(&t, bo, 1)
	writeIFDEntry(&t, bo, tagOrientation, tShort, 1, uint64(orientation))
	if len(thumb) > 0 {
		writeU32(&t, bo, uint32(ifd1Off))
	} else {
		writeU32(&t, bo, 0)
	}
	writeU16(&t, bo, 2)
	writeIFDEntry(&t, bo, tagJPEGIFOffset, tLong, 1, uint64(thumbOff))
	writeIFDEntry(&t, bo, tagJPEGIFLength, tLong, 1, uint64(len(thumb)))
	writeU32(&t, bo, 0)
	t.Write(thumb)
	return append([]byte("Exif\x00\x00"), t.Bytes()...)
}

// injectAPP1 splices an APP1 segment in right after the SOI of a real JPEG, the
// way a camera writes EXIF. image/jpeg ignores the segment; imaging reads the
// orientation out of it.
func injectAPP1(jpg, payload []byte) []byte {
	seg := make([]byte, 4, 4+len(payload))
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// ------------------------------------------------------------ TIFF container

type tiffEntry struct {
	tag, typ uint16
	count    uint32
	val      uint64
}

// buildTIFF lays out a TIFF container: the 8 byte header, one directory per
// element of counts chained through the "next" pointers, then the blobs. entries
// receives the offsets the directories and blobs landed at, so a fixture can
// point its locator tags at them.
func buildTIFF(bo binary.ByteOrder, counts []int, blobs [][]byte,
	entries func(dirOff, blobOff []int64) [][]tiffEntry) []byte {
	dirOff := make([]int64, len(counts))
	off := int64(8)
	for i, n := range counts {
		dirOff[i] = off
		off += int64(2 + 12*n + 4)
	}
	blobOff := make([]int64, len(blobs))
	for i, b := range blobs {
		blobOff[i] = off
		off += int64(len(b))
	}
	dirs := entries(dirOff, blobOff)
	if len(dirs) != len(counts) {
		panic("buildTIFF: directory count mismatch")
	}
	var f bytes.Buffer
	f.Write(tiffHeaderBytes(bo, dirOff[0]))
	for i, es := range dirs {
		if len(es) != counts[i] {
			panic("buildTIFF: entry count mismatch")
		}
		writeU16(&f, bo, uint16(len(es)))
		for _, e := range es {
			writeIFDEntry(&f, bo, e.tag, e.typ, e.count, e.val)
		}
		next := int64(0)
		if i+1 < len(dirs) {
			next = dirOff[i+1]
		}
		writeU32(&f, bo, uint32(next))
	}
	for _, b := range blobs {
		f.Write(b)
	}
	return f.Bytes()
}

// ------------------------------------------------------------ BMFF container

func makeBox(typ string, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	return append(b, payload...)
}

func makeUUIDBox(uuid string, payload []byte) []byte {
	inner := make([]byte, 0, 16+len(payload))
	inner = append(inner, []byte(uuid)[:16]...)
	return makeBox("uuid", append(inner, payload...))
}

// ----------------------------------------------------------- range reader stub

type memRanger struct {
	data     []byte
	requests int
	served   int64
}

func (m *memRanger) RangeRead(_ context.Context, r http_range.Range) (io.ReadCloser, error) {
	if r.Start < 0 || r.Start > int64(len(m.data)) {
		return nil, io.EOF
	}
	end := r.Start + r.Length
	if r.Length < 0 || end > int64(len(m.data)) {
		end = int64(len(m.data))
	}
	m.requests++
	m.served += end - r.Start
	return io.NopCloser(bytes.NewReader(m.data[r.Start:end])), nil
}

// ------------------------------------------------------------------ assertions

type wantCandidate struct {
	w, h        int
	source      string
	orientation int
}

func checkCandidates(t *testing.T, got []Candidate, want []wantCandidate) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d candidates %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		c := got[i]
		if c.Width != w.w || c.Height != w.h {
			t.Errorf("candidate %d: got %dx%d, want %dx%d", i, c.Width, c.Height, w.w, w.h)
		}
		if c.Source != w.source {
			t.Errorf("candidate %d: got source %q, want %q", i, c.Source, w.source)
		}
		if c.Orientation != w.orientation {
			t.Errorf("candidate %d: got orientation %d, want %d", i, c.Orientation, w.orientation)
		}
		if c.Length <= 0 {
			t.Errorf("candidate %d: got length %d", i, c.Length)
		}
	}
}

// ----------------------------------------------------------------- containers

// TestFindPreviewsTIFF covers the shape shared by CR2, NEF, ARW, DNG and the
// rest of the TIFF family: a directory chain whose entries locate the embedded
// renditions, one through JPEGIFOffset/Length and one through a JPEG compressed
// strip, with the nested EXIF thumbnail of the big one picked up as well.
func TestFindPreviewsTIFF(t *testing.T) {
	thumb := buildJPEG(160, 120, 0, nil, 0)
	big := buildJPEG(1600, 1200, 8, thumb, 1400)
	small := buildJPEG(320, 240, 0, nil, 400)

	f := buildTIFF(binary.LittleEndian, []int{3, 4}, [][]byte{big, small},
		func(_, blob []int64) [][]tiffEntry {
			return [][]tiffEntry{{
				{tagOrientation, tShort, 1, 6},
				{tagJPEGIFOffset, tLong, 1, uint64(blob[0])},
				{tagJPEGIFLength, tLong, 1, uint64(len(big))},
			}, {
				{tagCompression, tShort, 1, 6},
				{tagStripOffsets, tLong, 1, uint64(blob[1])},
				{tagOrientation, tShort, 1, 3},
				{tagStripByteCount, tLong, 1, uint64(len(small))},
			}}
		})

	cands, err := FindPreviews(bytes.NewReader(f), int64(len(f)), "IMG_0001.CR2")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	// Largest first, and the EXIF orientation of the embedded JPEG wins over the
	// one the container declares.
	checkCandidates(t, cands, []wantCandidate{
		{1600, 1200, "cr2", 8},
		{320, 240, "cr2", 3},
		{160, 120, "cr2+exifthumb", 8},
	})
	if cands[0].Length != int64(len(big)) {
		t.Errorf("got length %d for the full rendition, want %d", cands[0].Length, len(big))
	}
	if !bytes.Equal(f[cands[0].Offset:cands[0].Offset+cands[0].Length], big) {
		t.Error("the full rendition does not point at the embedded JPEG")
	}
	if !bytes.Equal(f[cands[2].Offset:cands[2].Offset+cands[2].Length], thumb) {
		t.Error("the EXIF thumbnail locator is off")
	}
}

// TestFindPreviewsPanasonic covers RW2, which stores the whole rendition in one
// oversized TIFF field instead of a locator pair.
func TestFindPreviewsPanasonic(t *testing.T) {
	jpg := buildJPEG(1920, 1440, 0, nil, 2000)
	f := buildTIFF(binary.LittleEndian, []int{1}, [][]byte{jpg},
		func(_, blob []int64) [][]tiffEntry {
			return [][]tiffEntry{{
				{tagJpgFromRaw, tUndefined, uint32(len(jpg)), uint64(blob[0])},
			}}
		})
	cands, err := FindPreviews(bytes.NewReader(f), int64(len(f)), "P1000123.RW2")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{1920, 1440, "rw2:jpgfromraw", 0}})
}

// TestFindPreviewsRAF covers Fujifilm's fixed header, which points straight at a
// full resolution rendition.
func TestFindPreviewsRAF(t *testing.T) {
	jpg := buildJPEG(1440, 960, 0, nil, 1200)
	head := make([]byte, 0x60)
	copy(head, "FUJIFILMCCD-RAW 0201FF129502")
	binary.BigEndian.PutUint32(head[0x54:0x58], uint32(len(head)))
	binary.BigEndian.PutUint32(head[0x58:0x5c], uint32(len(jpg)))
	f := append(head, jpg...)
	f = append(f, make([]byte, 4096)...) // sensor data

	cands, err := FindPreviews(bytes.NewReader(f), int64(len(f)), "DSCF1234.RAF")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{1440, 960, "raf", 0}})
	if cands[0].Offset != int64(len(head)) || cands[0].Length != int64(len(jpg)) {
		t.Errorf("got rendition @%d+%d, want @%d+%d",
			cands[0].Offset, cands[0].Length, len(head), len(jpg))
	}
}

// TestFindPreviewsCR3 covers Canon's ISO base media container: a THMB and a PRVW
// box nested in a vendor uuid box, plus the full size JPEG at the front of mdat.
func TestFindPreviewsCR3(t *testing.T) {
	thumb := buildJPEG(160, 120, 0, nil, 300)
	prvw := buildJPEG(1620, 1080, 0, nil, 700)
	full := buildJPEG(6000, 4000, 0, nil, 1500)

	// Each of these boxes prefixes its JPEG with a short vendor header.
	boxes := append(makeBox("THMB", append(make([]byte, 8), thumb...)),
		makeBox("PRVW", append(make([]byte, 12), prvw...))...)
	f := makeBox("ftyp", []byte("crx crx isom"))
	f = append(f, makeBox("moov", makeUUIDBox("0123456789abcdef", boxes))...)
	f = append(f, makeBox("mdat", append(full, make([]byte, 4096)...))...)

	cands, err := FindPreviews(bytes.NewReader(f), int64(len(f)), "IMG_5678.CR3")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{
		{6000, 4000, "cr3:mdat", 0},
		{1620, 1080, "cr3:PRVW", 0},
		{160, 120, "cr3:THMB", 0},
	})
	// The length comes from walking to EOI, so it must be exact rather than the
	// whole remainder of the box.
	if cands[0].Length != int64(len(full)) {
		t.Errorf("got mdat rendition length %d, want %d", cands[0].Length, len(full))
	}
	if !bytes.Equal(f[cands[1].Offset:cands[1].Offset+cands[1].Length], prvw) {
		t.Error("the PRVW rendition does not point at the embedded JPEG")
	}
}

// TestFindPreviewsCRW covers Canon's CIFF layout, whose directory sits at the end
// of the file and whose offsets are relative to the heap base.
func TestFindPreviewsCRW(t *testing.T) {
	jpg := buildJPEG(1536, 1024, 0, nil, 1200)
	const base = 26
	head := make([]byte, base)
	copy(head[0:2], "II")
	binary.LittleEndian.PutUint32(head[2:6], base)
	copy(head[6:14], "HEAPCCDR")
	copy(head[14:base], "0001CanonRaw")

	var f bytes.Buffer
	f.Write(head)
	f.Write(jpg)
	dirRel := int64(f.Len()) - base
	writeU16(&f, binary.LittleEndian, 1)
	e := make([]byte, 10)
	binary.LittleEndian.PutUint16(e[0:2], 0x2007) // JpegFromRaw
	binary.LittleEndian.PutUint32(e[2:6], uint32(len(jpg)))
	binary.LittleEndian.PutUint32(e[6:10], 0) // relative to the heap base
	f.Write(e)
	writeU32(&f, binary.LittleEndian, uint32(dirRel))

	b := f.Bytes()
	cands, err := FindPreviews(bytes.NewReader(b), int64(len(b)), "CRW_0001.CRW")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{1536, 1024, "crw", 0}})
	if cands[0].Offset != base {
		t.Errorf("got rendition offset %d, want %d", cands[0].Offset, base)
	}
}

// TestFindPreviewsMRW covers Minolta's block list, where the TTW block wraps a
// complete TIFF structure and every offset inside it is block relative.
func TestFindPreviewsMRW(t *testing.T) {
	jpg := buildJPEG(640, 480, 0, nil, 900)
	tiff := buildTIFF(binary.BigEndian, []int{2}, [][]byte{jpg},
		func(_, blob []int64) [][]tiffEntry {
			return [][]tiffEntry{{
				{tagJPEGIFOffset, tLong, 1, uint64(blob[0])},
				{tagJPEGIFLength, tLong, 1, uint64(len(jpg))},
			}}
		})
	var f bytes.Buffer
	f.WriteString("\x00MRM")
	writeU32(&f, binary.BigEndian, uint32(8+len(tiff)))
	f.WriteString("\x00TTW")
	writeU32(&f, binary.BigEndian, uint32(len(tiff)))
	f.Write(tiff)
	f.Write(make([]byte, 2048)) // sensor data

	b := f.Bytes()
	cands, err := FindPreviews(bytes.NewReader(b), int64(len(b)), "PICT0001.MRW")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{640, 480, "mrw", 0}})
	if !bytes.Equal(b[cands[0].Offset:cands[0].Offset+cands[0].Length], jpg) {
		t.Error("the block relative offset was not translated to a file offset")
	}
}

// TestFindPreviewsX3F covers Sigma's trailing section directory, whose sections
// start with a header the JPEG search has to skip over.
func TestFindPreviewsX3F(t *testing.T) {
	jpg := buildJPEG(2048, 1536, 0, nil, 1100)
	var f bytes.Buffer
	f.WriteString("FOVb")
	f.Write(make([]byte, 12))
	secOff := int64(f.Len())
	f.Write([]byte{0x01, 0x02, 0x03, 0x04}) // section header preceding the JPEG
	f.Write(jpg)
	secLen := int64(f.Len()) - secOff
	dirOff := int64(f.Len())
	f.WriteString("SECd")
	writeU32(&f, binary.LittleEndian, 0x00020001)
	writeU32(&f, binary.LittleEndian, 1)
	writeU32(&f, binary.LittleEndian, uint32(secOff))
	writeU32(&f, binary.LittleEndian, uint32(secLen))
	writeU32(&f, binary.LittleEndian, 0x494d4147)
	writeU32(&f, binary.LittleEndian, uint32(dirOff))

	b := f.Bytes()
	cands, err := FindPreviews(bytes.NewReader(b), int64(len(b)), "SDIM0001.X3F")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{2048, 1536, "x3f", 0}})
	if cands[0].Offset != secOff+4 {
		t.Errorf("got rendition offset %d, want %d", cands[0].Offset, secOff+4)
	}
}

// TestFindPreviewsScanFallback covers the last resort for a container none of the
// parsers recognise: search the head of the file for JPEG start markers.
func TestFindPreviewsScanFallback(t *testing.T) {
	jpg := buildJPEG(900, 600, 0, nil, 800)
	var f bytes.Buffer
	f.WriteString("ZZZZunknown container")
	f.Write(bytes.Repeat([]byte{0x11}, 1024))
	at := int64(f.Len())
	f.Write(jpg)
	f.Write(bytes.Repeat([]byte{0x22}, 512))

	b := f.Bytes()
	cands, err := FindPreviews(bytes.NewReader(b), int64(len(b)), "mystery.raw")
	if err != nil {
		t.Fatalf("FindPreviews: %v", err)
	}
	checkCandidates(t, cands, []wantCandidate{{900, 600, "scan", 0}})
	if cands[0].Offset != at || cands[0].Length != int64(len(jpg)) {
		t.Errorf("got rendition @%d+%d, want @%d+%d",
			cands[0].Offset, cands[0].Length, at, len(jpg))
	}
}

func TestFindPreviewsRejectsUnusable(t *testing.T) {
	small := make([]byte, 512)
	if _, err := FindPreviews(bytes.NewReader(small), int64(len(small)), "a.cr2"); err == nil {
		t.Error("a file too small to hold a preview was accepted")
	}
	blank := make([]byte, 8192)
	if _, err := FindPreviews(bytes.NewReader(blank), int64(len(blank)), "a.nef"); err == nil {
		t.Error("a file with no embedded JPEG was accepted")
	}
	// A locator pointing at something that is not a JPEG must be rejected too.
	f := buildTIFF(binary.LittleEndian, []int{2}, [][]byte{bytes.Repeat([]byte{0x7E}, 2048)},
		func(_, blob []int64) [][]tiffEntry {
			return [][]tiffEntry{{
				{tagJPEGIFOffset, tLong, 1, uint64(blob[0])},
				{tagJPEGIFLength, tLong, 1, 2048},
			}}
		})
	if _, err := FindPreviews(bytes.NewReader(f), int64(len(f)), "a.arw"); err == nil {
		t.Error("a locator pointing at non-JPEG data was accepted")
	}
}

// ------------------------------------------------------------------ JPEG probe

func TestProbeJPEG(t *testing.T) {
	thumb := buildJPEG(96, 64, 0, nil, 0)
	jpg := buildJPEG(800, 600, 5, thumb, 300)
	ra, size := bytes.NewReader(jpg), int64(len(jpg))

	info, ok := probeJPEG(ra, size, 0, size)
	if !ok {
		t.Fatal("probeJPEG rejected a valid stream")
	}
	if info.Width != 800 || info.Height != 600 {
		t.Errorf("got %dx%d, want 800x600", info.Width, info.Height)
	}
	if info.Orientation != 5 {
		t.Errorf("got orientation %d, want 5", info.Orientation)
	}
	if info.Length != size {
		t.Errorf("got length %d, want %d", info.Length, size)
	}
	if got := jpg[info.ThumbOffset : info.ThumbOffset+info.ThumbLength]; !bytes.Equal(got, thumb) {
		t.Errorf("the EXIF thumbnail locator points at %d+%d, which is not the thumbnail",
			info.ThumbOffset, info.ThumbLength)
	}

	// The header probe stops before the entropy coded data, so it knows the
	// dimensions but not the length.
	hdr, ok := probeJPEGHeader(ra, size, 0)
	if !ok || hdr.Width != 800 || hdr.Length != 0 {
		t.Errorf("probeJPEGHeader: got %+v, ok=%v", hdr, ok)
	}

	// A stream with no EXIF at all still yields dimensions.
	plain := buildJPEG(64, 32, 0, nil, 0)
	if info, ok = probeJPEG(bytes.NewReader(plain), int64(len(plain)), 0, int64(len(plain))); !ok ||
		info.Width != 64 || info.Orientation != 0 || info.ThumbLength != 0 {
		t.Errorf("probeJPEG on a plain stream: got %+v, ok=%v", info, ok)
	}
	if _, ok := probeJPEG(bytes.NewReader(plain), int64(len(plain)), 1, int64(len(plain))); ok {
		t.Error("probeJPEG accepted an offset that is not a SOI marker")
	}
}

// -------------------------------------------------------------- ordering, Pick

func TestNormalise(t *testing.T) {
	in := []Candidate{
		{Offset: 100, Length: 10, Width: 0, Height: 10, Source: "zero width"},
		{Offset: 200, Length: 0, Width: 10, Height: 10, Source: "zero length"},
		{Offset: -1, Length: 10, Width: 10, Height: 10, Source: "negative offset"},
		{Offset: 990, Length: 20, Width: 10, Height: 10, Source: "past the end"},
		{Offset: 300, Length: 30, Width: 100, Height: 100, Source: "small"},
		{Offset: 400, Length: 40, Width: 200, Height: 100, Source: "big"},
		{Offset: 400, Length: 99, Width: 200, Height: 100, Source: "duplicate offset"},
		{Offset: 500, Length: 80, Width: 100, Height: 100, Source: "same area, longer"},
	}
	got := normalise(in, 1000)
	want := []string{"big", "same area, longer", "small"}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if got[i].Source != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Source, w)
		}
	}
}

func TestPick(t *testing.T) {
	cands := []Candidate{
		{Width: 3000, Height: 2000, Length: 4 << 20, Source: "full"},
		{Width: 1600, Height: 1200, Length: 900 << 10, Source: "medium"},
		{Width: 160, Height: 120, Length: 8 << 10, Source: "tiny"},
	}
	if _, ok := Pick(nil, 0, 0); ok {
		t.Error("Pick accepted an empty list")
	}
	if c, _ := Pick(cands, 0, 0); c.Source != "full" {
		t.Errorf("unbounded pick: got %q, want the largest", c.Source)
	}
	if c, _ := Pick(cands, 0, 1<<20); c.Source != "medium" {
		t.Errorf("size capped pick: got %q, want the largest that fits", c.Source)
	}
	if c, _ := Pick(cands, 0, 1<<10); c.Source != "tiny" {
		t.Errorf("pick with a cap nothing fits: got %q, want the smallest", c.Source)
	}
	if c, _ := Pick(cands, 320, 0); c.Source != "medium" {
		t.Errorf("thumbnail pick: got %q, want the smallest that reaches the edge", c.Source)
	}
	if c, _ := Pick(cands, 4000, 0); c.Source != "full" {
		t.Errorf("thumbnail pick nothing reaches: got %q, want the largest", c.Source)
	}
}

// ------------------------------------------------------------------- the reader

func TestCachedReaderAt(t *testing.T) {
	data := make([]byte, 5*blockSize+123)
	for i := range data {
		data[i] = byte(i)
	}
	m := &memRanger{data: data}
	ra := NewCachedReaderAt(context.Background(), m, int64(len(data)))
	if ra.Size() != int64(len(data)) {
		t.Fatalf("got size %d, want %d", ra.Size(), len(data))
	}

	buf := make([]byte, 16)
	if n, err := ra.ReadAt(buf, 100); err != nil || n != len(buf) {
		t.Fatalf("ReadAt: %d bytes, %v", n, err)
	}
	if !bytes.Equal(buf, data[100:116]) {
		t.Error("ReadAt returned the wrong bytes")
	}
	if m.requests != 1 {
		t.Errorf("got %d range requests for one small read, want 1", m.requests)
	}

	// A second read inside a cached block costs nothing.
	if _, err := ra.ReadAt(buf, 200); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if m.requests != 1 {
		t.Errorf("got %d range requests, want the cached block to be reused", m.requests)
	}

	// A read straddling two missing blocks is coalesced into a single request.
	span := make([]byte, blockSize)
	if _, err := ra.ReadAt(span, 2*blockSize+64); err != nil {
		t.Fatalf("ReadAt across blocks: %v", err)
	}
	if !bytes.Equal(span, data[2*blockSize+64:3*blockSize+64]) {
		t.Error("ReadAt across blocks returned the wrong bytes")
	}
	if m.requests != 2 {
		t.Errorf("got %d range requests, want two adjacent blocks fetched together", m.requests)
	}

	if _, err := ra.ReadAt(buf, int64(len(data))); !errors.Is(err, io.EOF) {
		t.Errorf("reading at the end of the file: got %v, want io.EOF", err)
	}
	if read, reqs := ra.Stats(); read != m.served || reqs != m.requests {
		t.Errorf("Stats reports %d bytes in %d requests, the server saw %d in %d",
			read, reqs, m.served, m.requests)
	}
}

func TestCachedReaderAtBudget(t *testing.T) {
	data := make([]byte, 4*blockSize)
	m := &memRanger{data: data}
	ra := NewCachedReaderAt(context.Background(), m, int64(len(data)))
	ra.SetBudget(blockSize)

	buf := make([]byte, 8)
	if _, err := ra.ReadAt(buf, 0); err != nil {
		t.Fatalf("the first block should fit in the budget: %v", err)
	}
	if _, err := ra.ReadAt(buf, blockSize); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("got %v, want ErrBudgetExceeded", err)
	}
}

// TestLocateReadsOnlyMetadata is the property the whole package exists for: a
// preview is produced from container metadata and the rendition itself, not by
// pulling the original file through the server.
func TestLocateReadsOnlyMetadata(t *testing.T) {
	thumb := buildJPEG(160, 120, 0, nil, 0)
	big := buildJPEG(3000, 2000, 1, thumb, 200<<10)
	f := buildTIFF(binary.LittleEndian, []int{3}, [][]byte{big},
		func(_, blob []int64) [][]tiffEntry {
			return [][]tiffEntry{{
				{tagOrientation, tShort, 1, 1},
				{tagJPEGIFOffset, tLong, 1, uint64(blob[0])},
				{tagJPEGIFLength, tLong, 1, uint64(len(big))},
			}}
		})
	f = append(f, make([]byte, 8<<20)...) // the raw sensor data nobody has to read

	m := &memRanger{data: f}
	cands, read, requests, err := Locate(context.Background(), m, int64(len(f)), "DSC_0001.NEF")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(cands) != 2 || cands[0].Width != 3000 {
		t.Fatalf("got %v, want the full rendition and its EXIF thumbnail", cands)
	}
	if cands[0].Length != int64(len(big)) {
		t.Errorf("got rendition length %d, want %d", cands[0].Length, len(big))
	}
	if read > 512<<10 || requests > 4 {
		t.Errorf("located a preview in a %d byte file by reading %d bytes in %d requests",
			len(f), read, requests)
	}
	t.Logf("located %d renditions in a %d byte file: %d bytes in %d range requests",
		len(cands), len(f), read, requests)
}

// ---------------------------------------------------------------- thumbnailing

func TestMakeThumb(t *testing.T) {
	src := imaging.New(200, 80, color.NRGBA{R: 0x40, G: 0x80, B: 0xC0, A: 0xFF})
	var enc bytes.Buffer
	if err := imaging.Encode(&enc, src, imaging.JPEG); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	jpg := enc.Bytes()

	out, err := MakeThumb(jpg, 64, 0)
	if err != nil {
		t.Fatalf("MakeThumb: %v", err)
	}
	img, err := imaging.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() < 24 || b.Dy() > 28 {
		t.Errorf("got a %dx%d thumbnail, want 64 wide with the aspect kept", b.Dx(), b.Dy())
	}
	if len(out) >= len(jpg) {
		t.Errorf("the thumbnail (%d bytes) is not smaller than the source (%d)", len(out), len(jpg))
	}

	// Orientation supplied by the container is applied when the extracted JPEG
	// carries no EXIF of its own.
	rotated, err := MakeThumb(jpg, 4096, 6)
	if err != nil {
		t.Fatalf("MakeThumb with an orientation: %v", err)
	}
	img, err = imaging.Decode(bytes.NewReader(rotated))
	if err != nil {
		t.Fatalf("decode rotated: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 200 {
		t.Errorf("got %dx%d, want the 200x80 source rotated to 80x200", b.Dx(), b.Dy())
	}

	// When the JPEG does carry EXIF, the decoder has already honoured it, so the
	// container's value must not be applied a second time.
	withExif := injectAPP1(jpg, buildExifAPP1(binary.LittleEndian, 6, nil))
	once, err := MakeThumb(withExif, 4096, 6)
	if err != nil {
		t.Fatalf("MakeThumb on an EXIF carrying JPEG: %v", err)
	}
	img, err = imaging.Decode(bytes.NewReader(once))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 200 {
		t.Errorf("got %dx%d, want the rotation applied exactly once", b.Dx(), b.Dy())
	}
	if _, err := MakeThumb([]byte("not a jpeg at all"), 64, 0); err == nil {
		t.Error("MakeThumb accepted data that is not an image")
	}
}

// -------------------------------------------------------------------- settings

func TestSettings(t *testing.T) {
	restore := current.Load()
	t.Cleanup(func() { current.Store(restore) })

	for _, name := range []string{"a.CR3", "b.raf", "c.nef", "d.dng", "e.rw2", "f.arw", "g.crw", "h.cr2"} {
		if !Handles(name) {
			t.Errorf("Handles(%q) is false, the format is in the default list", name)
		}
	}
	for _, name := range []string{"a.jpg", "b.png", "noextension", "c.heic"} {
		if Handles(name) {
			t.Errorf("Handles(%q) is true, that is not a RAW container", name)
		}
	}
	if len(Extensions()) != len(DefaultExtensions) {
		t.Errorf("got %d extensions, want the %d defaults", len(Extensions()), len(DefaultExtensions))
	}

	// The list is replaced, not merged, and tolerates dots, spaces and blanks.
	SetExtensions([]string{".Foo", " bar ", "", "foo"})
	if !IsRaw("x.foo") || !IsRaw("x.BAR") {
		t.Error("SetExtensions did not normalise its input")
	}
	if IsRaw("x.cr3") {
		t.Error("SetExtensions merged with the defaults instead of replacing them")
	}
	SetExtensions(nil) // an empty list restores the built in defaults
	if !IsRaw("x.cr3") {
		t.Error("an empty extension list did not restore the defaults")
	}

	SetEnabled(false)
	if Handles("a.cr3") {
		t.Error("Handles is true while extraction is disabled")
	}
	if !IsRaw("a.cr3") {
		t.Error("IsRaw should test the name alone, independently of the switch")
	}
	if Negotiate() {
		t.Error("negotiation is active while extraction is disabled")
	}
	SetEnabled(true)
	if !Negotiate() {
		t.Error("negotiation is off although both switches are on")
	}
	SetNegotiate(false)
	if Negotiate() {
		t.Error("SetNegotiate(false) had no effect")
	}
}

func TestSettingLimitsAreClamped(t *testing.T) {
	restore := current.Load()
	t.Cleanup(func() { current.Store(restore) })

	SetThumbSize(1)
	if ThumbSize() != 32 {
		t.Errorf("got thumb size %d, want the lower bound 32", ThumbSize())
	}
	SetThumbSize(1 << 20)
	if ThumbSize() != 4096 {
		t.Errorf("got thumb size %d, want the upper bound 4096", ThumbSize())
	}
	SetThumbSize(256)
	if ThumbSize() != 256 {
		t.Errorf("got thumb size %d, want 256", ThumbSize())
	}
	SetMaxPreviewBytes(1)
	if MaxPreviewBytes() != 256<<10 {
		t.Errorf("got max preview %d, want the lower bound %d", MaxPreviewBytes(), 256<<10)
	}
	SetCacheBytes(-5)
	if CacheBytes() != 0 {
		t.Errorf("got cache size %d, want a negative value clamped to 0", CacheBytes())
	}
	SetCacheBytes(1 << 20)
	if CacheBytes() != 1<<20 {
		t.Errorf("got cache size %d, want %d", CacheBytes(), 1<<20)
	}
}
