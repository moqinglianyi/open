package rawpreview

import (
	"bytes"
	"fmt"
	"image"

	"github.com/disintegration/imaging"
)

// MakeThumb re-encodes an extracted JPEG as a small JPEG whose long edge is at
// most px pixels. orientation is applied only when the extracted JPEG carries no
// EXIF of its own, since imaging already honours EXIF when it is present.
func MakeThumb(jpg []byte, px, orientation int) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(jpg), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("decode embedded jpeg: %w", err)
	}
	if !bytes.Contains(jpg[:min(len(jpg), 4096)], []byte("Exif\x00\x00")) {
		img = applyOrientation(img, orientation)
	}
	b := img.Bounds()
	if w, h := b.Dx(), b.Dy(); w > px || h > px {
		if w >= h {
			img = imaging.Resize(img, px, 0, imaging.Lanczos)
		} else {
			img = imaging.Resize(img, 0, px, imaging.Lanczos)
		}
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}

func applyOrientation(img image.Image, o int) image.Image {
	switch o {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	}
	return img
}
