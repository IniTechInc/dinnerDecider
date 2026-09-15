// Package vision is the Linux port of DinnerDecider/Services/Vision: the
// geometry, orientation, decoding, preprocessing and OCR that turn a photo
// into the crops handed to the model.
//
// Everything here is CPU-only and cgo-free. There is no saliency detector and
// no rectangle detector on this platform, so Crops goes straight to the tile
// grid — see crop.go, and shared/pipeline/scan-pipeline.md
// § Ports without a saliency detector.
package vision

import "math"

// tileOverlap is the fraction each tile overlaps its neighbour.
//
// 0.20, from `CropService.tileOverlap`. The step between tiles is
// (1 - tileOverlap) of a tile, so tiles share 20% of their width with the tile
// to their left and 20% of their height with the tile above.
const tileOverlap = 0.20

// Rect is a rectangle in image pixel coordinates with the origin at the TOP
// LEFT, matching CGImage's coordinate space (and Go's image.Rectangle).
//
// It is float-valued rather than integer-valued on purpose: the tile grid steps
// by 0.8 * size/3, which is almost never a whole number, and rounding at each
// step instead of once at the end produces a different grid. Integral is the
// single rounding point, exactly as in the Swift.
type Rect struct {
	X, Y, W, H float64
}

// Integral returns the smallest whole-pixel rectangle that fully contains r.
//
// Port of CoreGraphics' `CGRect.integral`, which `CropService.crops` applies to
// every box before cropping:
//
//	x0 = floor(x)        y0 = floor(y)
//	x1 = ceil(x + w)     y1 = ceil(y + h)
//	w' = x1 - x0         h' = y1 - y0
//
// This is NOT int truncation of the origin and size. Truncating tile 1 of a
// 2016px-wide image gives {537, 672}; integral gives {537, 673} — the extra
// column is the fractional part of the origin being rounded DOWN while the far
// edge is rounded UP. Do not "simplify" this to int(x), int(w): the tiles would
// no longer tile the image and every crop after the first would be offset.
func (r Rect) Integral() Rect {
	x0 := math.Floor(r.X)
	y0 := math.Floor(r.Y)
	x1 := math.Ceil(r.X + r.W)
	y1 := math.Ceil(r.Y + r.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// TileGrid returns the overlapping tiles covering a width x height image.
//
// ⚠️ `CropService.tileGrid` is documented as a 3x3 grid but emits 16 tiles.
// Real shipped behavior, reproduced deliberately for parity. Do not "fix" it to
// 9 without re-running `eval/`.
//
// The source comment says "A 3x3 grid of overlapping tiles covering the whole
// image", but the loop below it steps by 0.8 * (size/3) and runs while the
// cursor is strictly less than the image size. Four steps fit before the cursor
// passes the edge — 0, 0.267, 0.533, 0.8 of the image — so the grid is 4x4 = 16
// tiles for any image of ordinary size (verified here for 2016x1512, 1512x2016
// and 900x900). The last column and last row are remainder tiles exactly 20% of
// the image wide/tall, because the cursor's fourth stop is at 0.8 of the image.
//
// This is transcribed from the LOOP, not from the comment. A port that emits 9
// tiles analyses different pixels, and since the 4-call scan budget means only
// the FIRST THREE tiles in emission order are ever sent to the model, the
// difference is not academic: with 16 tiles those three are the top-left, top-
// middle and top-right of the photo; with 9 they would be a third of the image
// each. Recall and the tuned confidence/dedupe constants were measured against
// the shipped geometry.
//
// Emission order is row-major with x on the inner loop: (0,0), (step,0),
// (2*step,0), (3*step,0), (0,stepY), ... Callers depend on it.
//
// Tiles are NOT padded — they already overlap by 20%.
//
// Rects are returned pre-Integral, in the same float coordinates the Swift
// produces; Crops applies Integral and the >= 8x8 check. The `> 8` guard here
// is the Swift's own, applied to the float size before rounding, and is why a
// small image yields fewer tiles (a 30x30 image gets 9, a 10x10 image gets 0).
func TileGrid(width, height int) []Rect {
	if width <= 0 || height <= 0 {
		return nil
	}
	w := float64(width)
	h := float64(height)

	tileWidth := w / 3.0
	tileHeight := h / 3.0
	stepX := tileWidth * (1 - tileOverlap)
	stepY := tileHeight * (1 - tileOverlap)
	if stepX <= 0 || stepY <= 0 {
		// Unreachable for width, height >= 1, but a zero step would spin
		// forever and this function runs on attacker-supplied dimensions.
		return nil
	}

	var rects []Rect
	for y := 0.0; y < h; y += stepY {
		for x := 0.0; x < w; x += stepX {
			tw := math.Min(tileWidth, w-x)
			th := math.Min(tileHeight, h-y)
			if tw > 8 && th > 8 {
				rects = append(rects, Rect{X: x, Y: y, W: tw, H: th})
			}
		}
	}
	return rects
}
