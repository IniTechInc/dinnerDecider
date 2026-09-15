package vision

import (
	"image"
	"image/draw"
	"math"
)

// paddingFraction is the outset applied to every DETECTED box, from
// `CropService.paddingFraction`.
//
// 0.15 per side, so a box grows 30% in each dimension. Proportional PER AXIS:
// a wide flat box gains more horizontal padding than vertical. Tiles are never
// padded — they already overlap by 20%.
const paddingFraction = 0.15

// minCropSide is the smallest usable crop, from `CropService.crops`.
//
// The tile grid tests `> 8` on the float size and this tests `>= 8` on the
// integral size. The two differ by design and are reproduced as they ship.
const minCropSide = 8

// CropResult is the set of crops taken from one photo.
//
// Rects are in source pixel coordinates with a top-left origin, whole-pixel
// (post-Integral), and index-parallel to Images: Rects[i] is where Images[i]
// came from, which is what lets the UI draw the boxes back over the photo.
type CropResult struct {
	Images []image.Image
	Rects  []Rect

	// IsTileFallback is true when the crops came from the tile grid rather
	// than from targeted detection.
	//
	// It is carried out of here because it CHOOSES THE PROMPT downstream:
	// a tile is a large region that may hold several things, so it gets
	// `identify-all-items`; a detected box is one object, so it gets
	// `identify-item`. Losing this flag does not fail loudly — it asks the
	// wrong question and gets a plausible wrong answer.
	IsTileFallback bool
}

// BoxDetector finds candidate object boxes in a photo.
//
// There is no implementation on Linux and there is not meant to be one yet;
// see Crops. Anyone adding one must return PIXEL rects with a TOP-LEFT origin,
// already pooled and merged, i.e. the equivalent of `CropService.detectBoxes`:
//
//   - Most vision frameworks (Apple's included) report NORMALISED rects with a
//     BOTTOM-LEFT origin. The conversion is x = minX*w, y = (1-maxY)*h,
//     and getting the flip wrong crops the wrong half of the photo while the
//     model dutifully describes whatever is there. A silent wrong answer, not
//     a crash — see shared/pipeline/scan-pipeline.md § Detection.
//   - Merging is GREEDY and ORDER-DEPENDENT: walk the pooled boxes in order,
//     union each into the first already-kept box whose IoU exceeds 0.3, and
//     stop. It is not a clustering algorithm and the order is part of it.
//
// Padding, the fallback decision and the minimum-size check are applied by
// Crops, not by the detector.
type BoxDetector interface {
	DetectBoxes(img image.Image) []Rect
}

// Crops splits a photo into the crops the scan will send to the model.
//
// Linux has no saliency detector and no rectangle detector, so this goes
// STRAIGHT TO THE TILE GRID with IsTileFallback = true. Per
// shared/pipeline/scan-pipeline.md § Ports without a saliency detector, that is
// not a degraded special case — it is a path iOS takes routinely, whenever
// detection yields fewer than two boxes, which on a packed fridge shelf is
// most of the time.
//
// Do NOT substitute a different object detector to "fix" this. Crop geometry
// changes recall, and the confidence and dedupe constants downstream were tuned
// against these crops; swapping the detector without re-running eval/ changes
// the pipeline's behaviour with no test that would notice.
//
// The image must already be orientation-normalised (use DecodeNormalized). The
// Swift normalises here because UIImage carries its rotation as metadata and
// may not be pixel-backed at all; neither is true of a decoded Go image.
//
// The returned crops SHARE PIXELS with img wherever the image type supports
// sub-imaging. They are read-only views, and holding one keeps the whole source
// bitmap alive — which is correct for the scan engine, where the crops are
// consumed and dropped together, and is the reason a 16-tile split of a 2016px
// photo costs nothing.
func Crops(img image.Image) CropResult {
	return CropsWithDetector(img, nil)
}

// CropsWithDetector is Crops with a detector wired in.
//
// Transcription of `CropService.crops`. The order is load-bearing and easy to
// get wrong: PAD FIRST, THEN COUNT. Padded boxes may overlap heavily but are
// NOT re-merged, and the `< 2` fallback test sees the padded set.
//
// Currently dead on Linux — nothing implements BoxDetector — and implemented
// and tested anyway, because it goes live the moment anyone wires a detector
// and because IsTileFallback = false is what selects the `identify-item`
// prompt. Follow-up F4.
func CropsWithDetector(img image.Image, detector BoxDetector) CropResult {
	if img == nil {
		return CropResult{}
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return CropResult{}
	}

	var boxes []Rect
	if detector != nil {
		boxes = detector.DetectBoxes(img)
		for i, box := range boxes {
			boxes[i] = padRect(box, width, height)
		}
	}

	isTileFallback := len(boxes) < 2
	if isTileFallback {
		boxes = TileGrid(width, height)
	}

	result := CropResult{IsTileFallback: isTileFallback}
	for _, box := range boxes {
		rect := box.Integral()
		if rect.W < minCropSide || rect.H < minCropSide {
			continue
		}
		cropped, ok := cropTo(img, rect)
		if !ok {
			continue
		}
		result.Images = append(result.Images, cropped)
		result.Rects = append(result.Rects, rect)
	}
	return result
}

// padRect outsets a detected box by paddingFraction on each axis and clamps it
// to the image.
//
// Port of `CropService.pad`. The outset is PROPORTIONAL PER AXIS — dx comes
// from the box's width and dy from its height — so a wide flat box gains more
// horizontal padding than vertical. Clamping is not optional: an unclamped crop
// rect is a crash on most imaging APIs, and here it would silently produce a
// crop smaller than its own rect.
func padRect(r Rect, width, height int) Rect {
	dx := r.W * paddingFraction
	dy := r.H * paddingFraction
	padded := Rect{X: r.X - dx, Y: r.Y - dy, W: r.W + 2*dx, H: r.H + 2*dy}
	return padded.intersect(Rect{W: float64(width), H: float64(height)})
}

// intersect returns the overlap of two rects, or the zero Rect when they do not
// overlap. The zero Rect then fails the minimum-size check, which is the same
// outcome CGRect.null produces on iOS.
func (r Rect) intersect(other Rect) Rect {
	x0 := math.Max(r.X, other.X)
	y0 := math.Max(r.Y, other.Y)
	x1 := math.Min(r.X+r.W, other.X+other.W)
	y1 := math.Min(r.Y+r.H, other.Y+other.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// subImager is implemented by every concrete image type in the standard
// library, and is how a crop avoids copying pixels.
type subImager interface {
	SubImage(r image.Rectangle) image.Image
}

// cropTo extracts rect from img, clamped to the image's bounds.
//
// rect must already be integral; the float-to-int conversions are exact for
// whole values. Reports false when nothing is left after clamping, which stands
// in for `CGImage.cropping(to:)` returning nil.
func cropTo(img image.Image, rect Rect) (image.Image, bool) {
	bounds := img.Bounds()
	clamped := image.Rect(
		bounds.Min.X+int(rect.X),
		bounds.Min.Y+int(rect.Y),
		bounds.Min.X+int(rect.X+rect.W),
		bounds.Min.Y+int(rect.Y+rect.H),
	).Intersect(bounds)
	if clamped.Empty() {
		return nil, false
	}

	if sub, ok := img.(subImager); ok {
		return sub.SubImage(clamped), true
	}
	// Fallback for an exotic image type: copy the region out.
	dst := image.NewNRGBA(image.Rect(0, 0, clamped.Dx(), clamped.Dy()))
	draw.Draw(dst, dst.Bounds(), img, clamped.Min, draw.Src)
	return dst, true
}
