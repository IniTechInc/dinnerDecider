package vision

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// fakeDetector stands in for the saliency + rectangle detectors Linux does not
// have, so the currently-dead identify-item path is still exercised.
type fakeDetector struct {
	boxes  []Rect
	called int
}

func (d *fakeDetector) DetectBoxes(image.Image) []Rect {
	d.called++
	out := make([]Rect, len(d.boxes))
	copy(out, d.boxes)
	return out
}

// gradientImage gives every pixel a colour derived from its coordinates, so a
// crop can be checked against the region it claims to come from.
func gradientImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 251), B: 7, A: 255})
		}
	}
	return img
}

// The headline behaviour for this platform: no detector, so the tile grid, so
// IsTileFallback = true and the identify-all-items prompt downstream.
func TestCropsGoesStraightToTheTileGrid(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2016, 1512))
	result := Crops(img)

	if !result.IsTileFallback {
		t.Fatal("IsTileFallback = false; a port with no detector must fall back to tiles")
	}
	if len(result.Images) != 16 {
		t.Fatalf("got %d crops, want 16", len(result.Images))
	}
	if len(result.Rects) != len(result.Images) {
		t.Fatalf("Rects (%d) and Images (%d) must stay index-parallel", len(result.Rects), len(result.Images))
	}
}

func TestCropsRectsAreIntegralAndMatchTheirImages(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2016, 1512))
	result := Crops(img)

	for i, rect := range result.Rects {
		if rect.X != math.Trunc(rect.X) || rect.Y != math.Trunc(rect.Y) ||
			rect.W != math.Trunc(rect.W) || rect.H != math.Trunc(rect.H) {
			t.Errorf("rect %d = %+v, want whole-pixel values", i, rect)
		}
		if rect.W < minCropSide || rect.H < minCropSide {
			t.Errorf("rect %d = %+v, below the %dx%d minimum", i, rect, minCropSide, minCropSide)
		}
		bounds := result.Images[i].Bounds()
		if float64(bounds.Dx()) != rect.W || float64(bounds.Dy()) != rect.H {
			t.Errorf("crop %d is %dx%d but its rect claims %vx%v", i, bounds.Dx(), bounds.Dy(), rect.W, rect.H)
		}
	}
}

// A crop whose rect is right but whose pixels come from elsewhere is the exact
// silent wrong-answer bug the coordinate-flip warning is about.
func TestCropsPixelsComeFromTheRegionTheRectNames(t *testing.T) {
	img := gradientImage(900, 900)
	result := Crops(img)

	for i, rect := range result.Rects {
		crop := result.Images[i]
		bounds := crop.Bounds()
		for _, offset := range [][2]int{{0, 0}, {1, 2}, {bounds.Dx() - 1, bounds.Dy() - 1}} {
			got := crop.At(bounds.Min.X+offset[0], bounds.Min.Y+offset[1])
			want := img.At(int(rect.X)+offset[0], int(rect.Y)+offset[1])
			if got != want {
				t.Fatalf("crop %d at %+v offset %v = %v, want %v", i, rect, offset, got, want)
			}
		}
	}
}

// The identify-item path. Dead on Linux today because nothing implements
// BoxDetector; it goes live the moment something does, and IsTileFallback =
// false is what switches the prompt.
func TestCropsWithDetectorUsesDetectedBoxesAndClearsTheFallbackFlag(t *testing.T) {
	img := gradientImage(1000, 800)
	detector := &fakeDetector{boxes: []Rect{
		{X: 100, Y: 100, W: 200, H: 200},
		{X: 600, Y: 400, W: 300, H: 200},
	}}

	result := CropsWithDetector(img, detector)

	if detector.called != 1 {
		t.Fatalf("detector called %d times, want 1", detector.called)
	}
	if result.IsTileFallback {
		t.Fatal("IsTileFallback = true with two detected boxes; the identify-item prompt would never be selected")
	}
	if len(result.Images) != 2 {
		t.Fatalf("got %d crops, want 2", len(result.Images))
	}
	// 15% per side on each axis: 200 -> 260, 300x200 -> 390x260.
	if got := result.Rects[0]; got.W != 260 || got.H != 260 {
		t.Errorf("box 0 padded to %vx%v, want 260x260", got.W, got.H)
	}
	if got := result.Rects[1]; got.W != 390 || got.H != 260 {
		t.Errorf("box 1 padded to %vx%v, want 390x260", got.W, got.H)
	}
}

// Fewer than TWO padded boxes triggers the tile grid — one box is not enough,
// which is the case a fridge shelf with a single prominent jar produces.
func TestCropsFallsBackWhenFewerThanTwoBoxes(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 900, 900))
	cases := []struct {
		name         string
		boxes        []Rect
		wantFallback bool
		wantCrops    int
	}{
		{"no boxes", nil, true, 16},
		{"one box", []Rect{{X: 10, Y: 10, W: 100, H: 100}}, true, 16},
		{"two boxes", []Rect{{X: 10, Y: 10, W: 100, H: 100}, {X: 500, Y: 500, W: 100, H: 100}}, false, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := CropsWithDetector(img, &fakeDetector{boxes: c.boxes})
			if result.IsTileFallback != c.wantFallback {
				t.Fatalf("IsTileFallback = %v, want %v", result.IsTileFallback, c.wantFallback)
			}
			if len(result.Images) != c.wantCrops {
				t.Fatalf("got %d crops, want %d", len(result.Images), c.wantCrops)
			}
		})
	}
}

// Pad first, then count — and do NOT re-merge. Two boxes that swallow each
// other once padded are still two crops and still not a tile fallback.
func TestCropsPadsBeforeCountingAndDoesNotRemerge(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 900, 900))
	detector := &fakeDetector{boxes: []Rect{
		{X: 100, Y: 100, W: 400, H: 400},
		{X: 110, Y: 110, W: 400, H: 400}, // ~95% IoU: a merger would fuse these
	}}

	result := CropsWithDetector(img, detector)

	if result.IsTileFallback {
		t.Fatal("IsTileFallback = true; two heavily overlapping padded boxes are still two boxes")
	}
	if len(result.Images) != 2 {
		t.Fatalf("got %d crops, want 2 (padded boxes are never re-merged)", len(result.Images))
	}
}

// Proportional per axis, so a wide flat box gains more horizontal padding.
func TestPadRectIsProportionalPerAxisAndClampsToBounds(t *testing.T) {
	cases := []struct {
		name string
		in   Rect
		w, h int
		want Rect
	}{
		{
			"wide flat box gains more horizontally",
			Rect{X: 200, Y: 400, W: 400, H: 100}, 1000, 1000,
			Rect{X: 140, Y: 385, W: 520, H: 130},
		},
		{
			"tall box gains more vertically",
			Rect{X: 400, Y: 200, W: 100, H: 400}, 1000, 1000,
			Rect{X: 385, Y: 140, W: 130, H: 520},
		},
		{
			"padding past the top-left is clamped away",
			Rect{X: 0, Y: 0, W: 100, H: 100}, 1000, 1000,
			Rect{X: 0, Y: 0, W: 115, H: 115},
		},
		{
			"padding past the bottom-right is clamped away",
			Rect{X: 900, Y: 900, W: 100, H: 100}, 1000, 1000,
			Rect{X: 885, Y: 885, W: 115, H: 115},
		},
		{
			"a box entirely outside collapses to zero",
			Rect{X: 2000, Y: 2000, W: 100, H: 100}, 1000, 1000,
			Rect{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := padRect(c.in, c.w, c.h)
			if math.Abs(got.X-c.want.X) > rectEpsilon || math.Abs(got.Y-c.want.Y) > rectEpsilon ||
				math.Abs(got.W-c.want.W) > rectEpsilon || math.Abs(got.H-c.want.H) > rectEpsilon {
				t.Fatalf("padRect(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// The >= 8x8 check runs AFTER Integral, and a dropped box must drop from both
// slices or the UI draws boxes over the wrong crops.
func TestCropsDropsBoxesBelowTheMinimumAndKeepsTheSlicesParallel(t *testing.T) {
	img := gradientImage(900, 900)
	detector := &fakeDetector{boxes: []Rect{
		{X: 100, Y: 100, W: 200, H: 200},   // survives
		{X: 500, Y: 500, W: 1, H: 1},       // 1x1 pads to 1.3x1.3, integral 3x3: dropped
		{X: 700, Y: 700, W: 100, H: 100},   // survives
		{X: 4000, Y: 4000, W: 100, H: 100}, // entirely outside: dropped
	}}

	result := CropsWithDetector(img, detector)

	if len(result.Images) != 2 {
		t.Fatalf("got %d crops, want 2", len(result.Images))
	}
	if len(result.Rects) != 2 {
		t.Fatalf("got %d rects, want 2", len(result.Rects))
	}
	for i := range result.Images {
		bounds := result.Images[i].Bounds()
		if float64(bounds.Dx()) != result.Rects[i].W || float64(bounds.Dy()) != result.Rects[i].H {
			t.Errorf("crop %d and rect %d disagree: %v vs %+v", i, i, bounds, result.Rects[i])
		}
	}
}

// A detector that reports a box hanging off the edge must produce a crop that
// exists, not a panic and not a rect wider than its own pixels.
func TestCropsClampsBoxesToImageBounds(t *testing.T) {
	img := gradientImage(400, 300)
	detector := &fakeDetector{boxes: []Rect{
		{X: -50, Y: -50, W: 200, H: 200},
		{X: 350, Y: 250, W: 200, H: 200},
	}}

	result := CropsWithDetector(img, detector)

	if len(result.Images) != 2 {
		t.Fatalf("got %d crops, want 2", len(result.Images))
	}
	for i, rect := range result.Rects {
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 400 || rect.Y+rect.H > 300 {
			t.Errorf("rect %d = %+v escapes the 400x300 image", i, rect)
		}
		bounds := result.Images[i].Bounds()
		if float64(bounds.Dx()) != rect.W || float64(bounds.Dy()) != rect.H {
			t.Errorf("crop %d is %dx%d, rect claims %vx%v", i, bounds.Dx(), bounds.Dy(), rect.W, rect.H)
		}
	}
}

func TestCropsOnDegenerateImages(t *testing.T) {
	t.Run("nil image", func(t *testing.T) {
		result := Crops(nil)
		if len(result.Images) != 0 || result.IsTileFallback {
			t.Fatalf("Crops(nil) = %+v, want an empty non-fallback result", result)
		}
	})
	t.Run("zero-sized image", func(t *testing.T) {
		result := Crops(image.NewNRGBA(image.Rect(0, 0, 0, 0)))
		if len(result.Images) != 0 || result.IsTileFallback {
			t.Fatalf("Crops(0x0) = %+v, want an empty non-fallback result", result)
		}
	})
	t.Run("image too small to tile", func(t *testing.T) {
		// Parity with the Swift: detection yields nothing, so the fallback
		// flag is set, and only then does the tile grid come back empty.
		result := Crops(image.NewNRGBA(image.Rect(0, 0, 10, 10)))
		if !result.IsTileFallback {
			t.Error("IsTileFallback = false; the fallback decision happens before the grid is built")
		}
		if len(result.Images) != 0 {
			t.Errorf("got %d crops from a 10x10 image, want 0", len(result.Images))
		}
	})
}

// A non-zero-origin source (what SubImage returns, so what a crop of a crop
// would be) must still be cropped correctly.
func TestCropsHandlesANonZeroOriginSource(t *testing.T) {
	full := gradientImage(2000, 2000)
	offset := full.SubImage(image.Rect(500, 400, 1400, 1300)) // 900x900 at (500,400)

	result := Crops(offset)

	if len(result.Images) != 16 {
		t.Fatalf("got %d crops, want 16", len(result.Images))
	}
	rect := result.Rects[1]
	crop := result.Images[1]
	bounds := crop.Bounds()
	got := crop.At(bounds.Min.X, bounds.Min.Y)
	want := full.At(500+int(rect.X), 400+int(rect.Y))
	if got != want {
		t.Fatalf("crop 1 top-left = %v, want %v (rects are relative to the source's own origin)", got, want)
	}
}

// The scan budget only ever analyses the first three crops, so their identity
// is part of the pipeline's behaviour rather than an implementation detail.
func TestCropsEmissionOrderSurvivesIntoTheResult(t *testing.T) {
	result := Crops(image.NewNRGBA(image.Rect(0, 0, 2016, 1512)))
	want := []Rect{
		{X: 0, Y: 0, W: 672, H: 504},
		{X: 537, Y: 0, W: 673, H: 504},
		{X: 1075, Y: 0, W: 673, H: 504},
	}
	for i, expected := range want {
		if !rectsClose(result.Rects[i], expected) {
			t.Errorf("crop %d rect = %+v, want %+v", i, result.Rects[i], expected)
		}
	}
}
