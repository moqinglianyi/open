package rawpreview

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// RangeReader is the subset of model.RangeReaderIF this package needs. Keeping
// it local avoids coupling the parsers to the storage model.
type RangeReader interface {
	RangeRead(ctx context.Context, r http_range.Range) (io.ReadCloser, error)
}

// ErrBudgetExceeded is returned once a parse has read MaxParseBytes.
var ErrBudgetExceeded = errors.New("rawpreview: read budget exceeded")

const blockSize = 128 << 10

// CachedReaderAt turns a RangeReader into a random access reader, coalescing
// nearby reads into block aligned range requests and caching a bounded number
// of blocks. RAW metadata parsing jumps around the file, so without this every
// IFD field would cost one HTTP request.
//
// Not safe for concurrent use.
type CachedReaderAt struct {
	ctx       context.Context
	rr        RangeReader
	size      int64
	budget    int64
	maxBlocks int

	blocks   map[int64][]byte
	order    []int64
	read     int64
	requests int
}

// NewCachedReaderAt wraps rr. size must be the full file size.
func NewCachedReaderAt(ctx context.Context, rr RangeReader, size int64) *CachedReaderAt {
	return &CachedReaderAt{
		ctx:       ctx,
		rr:        rr,
		size:      size,
		budget:    MaxParseBytes,
		maxBlocks: 64,
		blocks:    make(map[int64][]byte),
	}
}

// Size returns the underlying file size.
func (c *CachedReaderAt) Size() int64 { return c.size }

// Stats reports how much was transferred and how many range requests were made.
func (c *CachedReaderAt) Stats() (bytes int64, requests int) { return c.read, c.requests }

// SetBudget overrides the total byte budget for this reader.
func (c *CachedReaderAt) SetBudget(n int64) { c.budget = n }

func (c *CachedReaderAt) evict() {
	for len(c.order) > c.maxBlocks {
		delete(c.blocks, c.order[0])
		c.order = c.order[1:]
	}
}

// fetch loads every missing block covering [start, end) using as few range
// requests as possible.
func (c *CachedReaderAt) fetch(start, end int64) error {
	first := start / blockSize
	last := (end - 1) / blockSize
	for i := first; i <= last; i++ {
		if _, ok := c.blocks[i]; ok {
			continue
		}
		// Extend the run over consecutive missing blocks.
		j := i
		for j+1 <= last {
			if _, ok := c.blocks[j+1]; ok {
				break
			}
			j++
		}
		off := i * blockSize
		length := min((j-i+1)*blockSize, c.size-off)
		if length <= 0 {
			return io.EOF
		}
		if c.read+length > c.budget {
			return ErrBudgetExceeded
		}
		buf, err := c.request(off, length)
		if err != nil {
			return err
		}
		c.read += int64(len(buf))
		for n := int64(0); n < int64(len(buf)); n += blockSize {
			c.blocks[(off+n)/blockSize] = buf[n:min(n+blockSize, int64(len(buf)))]
			c.order = append(c.order, (off+n)/blockSize)
		}
		c.evict()
		i = j
	}
	return nil
}

func (c *CachedReaderAt) request(off, length int64) ([]byte, error) {
	rc, err := c.rr.RangeRead(c.ctx, http_range.Range{Start: off, Length: length})
	if err != nil {
		return nil, fmt.Errorf("rawpreview: range read at %d+%d: %w", off, length, err)
	}
	defer rc.Close()
	c.requests++
	buf := make([]byte, length)
	n, err := io.ReadFull(rc, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rawpreview: range read at %d+%d: %w", off, length, err)
	}
	if n == 0 {
		return nil, io.EOF
	}
	return buf[:n], nil
}

// ReadAt implements io.ReaderAt.
func (c *CachedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("rawpreview: negative offset")
	}
	if off >= c.size {
		return 0, io.EOF
	}
	end := min(off+int64(len(p)), c.size)
	if err := c.fetch(off, end); err != nil {
		return 0, err
	}
	n := 0
	for n < int(end-off) {
		pos := off + int64(n)
		b, ok := c.blocks[pos/blockSize]
		if !ok {
			return n, io.ErrUnexpectedEOF
		}
		in := pos % blockSize
		if in >= int64(len(b)) {
			return n, io.EOF
		}
		n += copy(p[n:end-off], b[in:])
	}
	if off+int64(n) >= c.size && n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// slice reads exactly length bytes at off, clamped to the file size.
func slice(ra io.ReaderAt, size, off, length int64) ([]byte, error) {
	if off < 0 || off >= size {
		return nil, io.EOF
	}
	length = min(length, size-off)
	if length <= 0 {
		return nil, io.EOF
	}
	buf := make([]byte, length)
	n, err := ra.ReadAt(buf, off)
	if n > 0 {
		return buf[:n], nil
	}
	if err == nil {
		err = io.EOF
	}
	return nil, err
}
