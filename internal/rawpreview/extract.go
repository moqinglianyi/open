package rawpreview

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Candidate locates one JPEG rendition inside a RAW container.
type Candidate struct {
	Offset      int64
	Length      int64
	Width       int
	Height      int
	Orientation int // EXIF orientation value, 0 when unknown
	Source      string
}

func (c Candidate) area() int64 { return int64(c.Width) * int64(c.Height) }

// String renders a candidate for logging.
func (c Candidate) String() string {
	return fmt.Sprintf("%dx%d %s @%d+%d", c.Width, c.Height, c.Source, c.Offset, c.Length)
}

// scanCandidates is the last resort for containers this package cannot parse:
// it searches the head of the file for JPEG start markers and validates every
// hit by walking the marker structure.
func scanCandidates(ra io.ReaderAt, size int64, out *[]Candidate) bool {
	const chunk = 256 << 10
	limit := min(size, MaxScanBytes)
	found := 0
	for pos := int64(0); pos < limit && found < 6; {
		b, err := slice(ra, size, pos, min(chunk, limit-pos))
		if err != nil || len(b) < 3 {
			break
		}
		j := bytes.Index(b, []byte{0xFF, 0xD8, 0xFF})
		if j < 0 {
			if int64(len(b)) <= 2 {
				break
			}
			pos += int64(len(b)) - 2 // overlap, a marker may straddle the chunk
			continue
		}
		at := pos + int64(j)
		before := len(*out)
		if addProbed(ra, size, at, min(size-at, MaxParseBytes), "scan", out) && len(*out) > before {
			found++
			c := (*out)[before]
			pos = c.Offset + c.Length
			continue
		}
		pos = at + 3
	}
	return found > 0
}

// FindPreviews locates the embedded JPEG renditions of a RAW file, largest
// first. ra must cover the whole file and size must be its exact length.
func FindPreviews(ra io.ReaderAt, size int64, name string) ([]Candidate, error) {
	if size < 1024 {
		return nil, fmt.Errorf("rawpreview: %s is too small to hold a preview (%d bytes)", name, size)
	}
	ext := strings.ToLower(strings.TrimPrefix(utilsExt(name), "."))
	magic, err := slice(ra, size, 0, 16)
	if err != nil {
		return nil, fmt.Errorf("rawpreview: read header of %s: %w", name, err)
	}

	var out []Candidate
	ok := false
	switch {
	case len(magic) >= 16 && string(magic[0:16]) == "FUJIFILMCCD-RAW ":
		ok = rafCandidates(ra, size, &out)
	case len(magic) >= 8 && string(magic[4:8]) == "ftyp":
		ok = cr3Candidates(ra, size, &out)
	case len(magic) >= 14 && string(magic[6:14]) == "HEAPCCDR":
		ok = crwCandidates(ra, size, &out)
	case len(magic) >= 4 && string(magic[0:4]) == "\x00MRM":
		ok = mrwCandidates(ra, size, &out)
	case len(magic) >= 4 && string(magic[0:4]) == "FOVb":
		ok = x3fCandidates(ra, size, &out)
	default:
		// Everything else in the RAW family is a TIFF variant.
		ok = tiffCandidates(ra, size, ext, &out)
	}
	if !ok || len(out) == 0 {
		scanCandidates(ra, size, &out)
	}

	out = normalise(out, size)
	if len(out) == 0 {
		return nil, fmt.Errorf("rawpreview: no embedded JPEG found in %s", name)
	}
	return out, nil
}

// normalise drops unusable entries, removes duplicates and orders the result
// from largest to smallest rendition.
func normalise(in []Candidate, size int64) []Candidate {
	seen := make(map[int64]bool, len(in))
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		if c.Width <= 0 || c.Height <= 0 || c.Length <= 0 {
			continue
		}
		if c.Offset < 0 || c.Offset+c.Length > size {
			continue
		}
		if seen[c.Offset] {
			continue
		}
		seen[c.Offset] = true
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		if n := cmp.Compare(b.area(), a.area()); n != 0 {
			return n
		}
		return cmp.Compare(b.Length, a.Length)
	})
	return out
}

// Pick chooses a rendition. minEdge <= 0 asks for the largest one that fits in
// maxBytes; otherwise the smallest rendition whose short edge reaches minEdge
// is preferred, which keeps thumbnail generation cheap.
func Pick(cands []Candidate, minEdge int, maxBytes int64) (Candidate, bool) {
	if len(cands) == 0 {
		return Candidate{}, false
	}
	if minEdge <= 0 {
		for _, c := range cands { // already sorted largest first
			if maxBytes <= 0 || c.Length <= maxBytes {
				return c, true
			}
		}
		return cands[len(cands)-1], true
	}
	best := -1
	for i, c := range cands {
		if min(c.Width, c.Height) < minEdge {
			continue
		}
		if best < 0 || c.area() < cands[best].area() {
			best = i
		}
	}
	if best >= 0 {
		return cands[best], true
	}
	return cands[0], true
}

// Locate finds the embedded renditions of a remote RAW file, reading only the
// container metadata through rr. It also reports how much was transferred and
// how many range requests that took.
func Locate(ctx context.Context, rr RangeReader, size int64, name string) ([]Candidate, int64, int, error) {
	ra := NewCachedReaderAt(ctx, rr, size)
	cands, err := FindPreviews(ra, size, name)
	read, requests := ra.Stats()
	return cands, read, requests, err
}

func utilsExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}
