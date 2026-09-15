package vision

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"testing"
)

// tinyWebP is a 10x6 solid rgb(42,127,200) lossless VP8L WebP, hand-assembled
// for this test because x/image ships a decoder and no encoder, and go.mod is
// frozen. 34 bytes.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA4AAAAvCUABAOhfVYrc/wIAAA=="

func mustWebP(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatalf("decoding the embedded WebP fixture: %v", err)
	}
	return data
}

// solidNRGBA fills via the Pix slice rather than SetNRGBA: some of these
// fixtures are 12 megapixels and the per-pixel path is minutes under -race.
func solidNRGBA(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if len(img.Pix) == 0 {
		return img
	}
	copy(img.Pix, []byte{c.R, c.G, c.B, c.A})
	for filled := 4; filled < len(img.Pix); filled *= 2 {
		copy(img.Pix[filled:], img.Pix[:filled])
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// encodeJPEGWithOrientation encodes img and splices an EXIF APP1 carrying the
// given orientation in directly after SOI, which is where a camera puts it.
func encodeJPEGWithOrientation(t *testing.T, img image.Image, orientation uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	raw := buf.Bytes()
	if raw[0] != 0xFF || raw[1] != 0xD8 {
		t.Fatalf("jpeg.Encode did not start with SOI")
	}
	out := append([]byte{}, raw[:2]...)
	out = append(out, exifSegment(validExif(orientation).tiff())...)
	return append(out, raw[2:]...)
}

// --- Tone curve ---------------------------------------------------------

// The LUT must exist before anything calls applyToneCurve: it is a
// package-level array built at init, not per crop. Reading it first thing in a
// test is the cheapest observable form of that assertion.
func TestToneCurveLUTIsBuiltOnceAtPackageInit(t *testing.T) {
	if toneLUT[255] != 255 || toneLUT[0] == 0 {
		t.Fatalf("toneLUT looks unbuilt at test time: [0]=%d [255]=%d", toneLUT[0], toneLUT[255])
	}
	// And rebuilding it is deterministic, so nothing depends on call order.
	if rebuilt := buildToneCurveLUT(); rebuilt != toneLUT {
		t.Fatal("buildToneCurveLUT is not deterministic")
	}
}

func TestToneCurveLUTEndpoints(t *testing.T) {
	// 0 lifts to 0.05 of full scale: the shadow lift the curve exists for.
	if want := uint8(math.Round(0.05 * 255)); toneLUT[0] != want {
		t.Errorf("toneLUT[0] = %d, want %d (0.05 * 255)", toneLUT[0], want)
	}
	// The white point is pinned, so highlights do not blow out.
	if toneLUT[255] != 255 {
		t.Errorf("toneLUT[255] = %d, want 255 (the white point is pinned)", toneLUT[255])
	}
}

// The property that actually has to hold. A tone curve that dips anywhere
// inverts contrast in a band of the image, which is exactly the failure a
// natural cubic spline through these points would produce.
func TestToneCurveLUTIsMonotonicNonDecreasing(t *testing.T) {
	for i := 1; i < 256; i++ {
		if toneLUT[i] < toneLUT[i-1] {
			t.Fatalf("toneLUT is not monotonic: [%d]=%d then [%d]=%d", i-1, toneLUT[i-1], i, toneLUT[i])
		}
	}
	// And it is a lift, not a no-op: the midtones really do move.
	if toneLUT[128] <= 128 {
		t.Errorf("toneLUT[128] = %d, want > 128 (the curve lifts midtones)", toneLUT[128])
	}
}

// Every control point must be reproduced within one 8-bit step. The LUT is
// indexed by i/255, so a control point at 0.25 lands between entries; the
// nearest entry is compared against the control point's exact value.
func TestToneCurveLUTHitsTheFiveControlPoints(t *testing.T) {
	const tolerance = 1.0 // one 8-bit step, i.e. 1/255 in normalised units
	for _, point := range toneCurveControlPoints {
		index := int(math.Round(point[0] * 255))
		want := point[1] * 255
		got := float64(toneLUT[index])
		if math.Abs(got-want) > tolerance {
			t.Errorf("control point (%.2f, %.2f): toneLUT[%d] = %.0f, want %.2f (+/- 1/255)", point[0], point[1], index, got, want)
		}
	}
}

func TestApplyToneCurveLeavesTheCallerBitmapAlone(t *testing.T) {
	src := solidNRGBA(4, 4, color.NRGBA{R: 0, G: 64, B: 200, A: 255})
	before := append([]byte{}, src.Pix...)

	out := applyToneCurve(src)

	if !bytes.Equal(src.Pix, before) {
		t.Fatal("applyToneCurve modified the caller's bitmap")
	}
	if got, want := out.NRGBAAt(0, 0).R, toneLUT[0]; got != want {
		t.Errorf("R = %d, want %d", got, want)
	}
	if got, want := out.NRGBAAt(0, 0).G, toneLUT[64]; got != want {
		t.Errorf("G = %d, want %d", got, want)
	}
	if got, want := out.NRGBAAt(0, 0).B, toneLUT[200]; got != want {
		t.Errorf("B = %d, want %d", got, want)
	}
	if got := out.NRGBAAt(0, 0).A; got != 255 {
		t.Errorf("A = %d, want 255 (the curve must not touch alpha)", got)
	}
}

// --- Decode -------------------------------------------------------------

func TestDecodeSupportedFormats(t *testing.T) {
	source := solidNRGBA(10, 6, color.NRGBA{R: 42, G: 127, B: 200, A: 255})

	cases := []struct {
		name string
		data []byte
	}{
		{"png", encodePNG(t, source)},
		{"jpeg", encodeJPEGWithOrientation(t, source, 1)},
		{"webp", mustWebP(t)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectFormat(c.data); got != c.name {
				t.Fatalf("DetectFormat = %q, want %q", got, c.name)
			}
			img, err := Decode(c.data)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got := img.Bounds().Dx(); got != 10 {
				t.Errorf("width = %d, want 10", got)
			}
			if got := img.Bounds().Dy(); got != 6 {
				t.Errorf("height = %d, want 6", got)
			}
		})
	}
}

// HEIC and TIFF are refused BY NAME so /api can tell the user which it was and
// what to do (HEIC is transcoded in the browser before upload; TIFF is simply
// not an accepted format).
func TestDecodeRejectsUnsupportedFormats(t *testing.T) {
	heic := append([]byte{0, 0, 0, 0x20}, []byte("ftypheic")...)
	heic = append(heic, make([]byte, 64)...)
	avif := append([]byte{0, 0, 0, 0x20}, []byte("ftypavif")...)
	avif = append(avif, make([]byte, 64)...)

	cases := []struct {
		name   string
		data   []byte
		format string
	}{
		{"heic", heic, "heic"},
		{"avif", avif, "avif"},
		{"tiff little-endian", append([]byte("II*\x00"), make([]byte, 64)...), "tiff"},
		{"tiff big-endian", append([]byte("MM\x00*"), make([]byte, 64)...), "tiff"},
		{"gif", append([]byte("GIF89a"), make([]byte, 64)...), "gif"},
		{"bmp", append([]byte("BM"), make([]byte, 64)...), "bmp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectFormat(c.data); got != c.format {
				t.Errorf("DetectFormat = %q, want %q", got, c.format)
			}
			_, err := Decode(c.data)
			if !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("Decode error = %v, want ErrUnsupportedFormat", err)
			}
			// The API layer builds its message from the name, so the name
			// has to survive the wrapping.
			if !bytes.Contains([]byte(err.Error()), []byte(c.format)) {
				t.Errorf("error %q does not name the format %q", err, c.format)
			}
		})
	}
}

func TestDecodeRejectsGarbageAndEmptyInput(t *testing.T) {
	for _, c := range [][]byte{nil, {}, []byte("not an image"), {0xFF, 0xD8}} {
		if _, err := Decode(c); !errors.Is(err, ErrUnsupportedFormat) {
			t.Errorf("Decode(%q) error = %v, want ErrUnsupportedFormat", c, err)
		}
	}
}

// A well-formed header with a corrupt body is a DECODE failure, not an
// unsupported format: the distinction drives two different messages.
func TestDecodeReportsCorruptDataSeparatelyFromUnsupportedFormat(t *testing.T) {
	corrupt := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	_, err := Decode(corrupt)
	if err == nil {
		t.Fatal("Decode(corrupt PNG) = nil error")
	}
	if errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("corrupt PNG reported as unsupported format: %v", err)
	}
}

// --- Orientation --------------------------------------------------------

// A 3x2 source whose every pixel is uniquely identifiable, so a transform can
// be checked pixel by pixel rather than by eye.
func orientationProbe() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 0, A: 255})
		}
	}
	return img
}

func TestNormalizeOrientationAllEightTransforms(t *testing.T) {
	src := orientationProbe()
	w, h := 3, 2

	// want[dy][dx] = the (srcX, srcY) that must land there.
	cases := []struct {
		orientation int
		width       int
		height      int
		source      func(dx, dy int) (int, int)
	}{
		{1, w, h, func(dx, dy int) (int, int) { return dx, dy }},
		{2, w, h, func(dx, dy int) (int, int) { return w - 1 - dx, dy }},
		{3, w, h, func(dx, dy int) (int, int) { return w - 1 - dx, h - 1 - dy }},
		{4, w, h, func(dx, dy int) (int, int) { return dx, h - 1 - dy }},
		{5, h, w, func(dx, dy int) (int, int) { return dy, dx }},
		{6, h, w, func(dx, dy int) (int, int) { return dy, h - 1 - dx }},
		{7, h, w, func(dx, dy int) (int, int) { return w - 1 - dy, h - 1 - dx }},
		{8, h, w, func(dx, dy int) (int, int) { return w - 1 - dy, dx }},
	}
	for _, c := range cases {
		out := NormalizeOrientation(src, c.orientation)
		bounds := out.Bounds()
		if bounds.Dx() != c.width || bounds.Dy() != c.height {
			t.Errorf("orientation %d: size %dx%d, want %dx%d", c.orientation, bounds.Dx(), bounds.Dy(), c.width, c.height)
			continue
		}
		for dy := 0; dy < c.height; dy++ {
			for dx := 0; dx < c.width; dx++ {
				sx, sy := c.source(dx, dy)
				if got, want := out.At(dx, dy), src.At(sx, sy); got != want {
					t.Errorf("orientation %d: dst(%d,%d) = %v, want src(%d,%d) = %v", c.orientation, dx, dy, got, sx, sy, want)
				}
			}
		}
	}
}

func TestNormalizeOrientationPassesThroughUnusableValues(t *testing.T) {
	src := orientationProbe()
	for _, orientation := range []int{-1, 0, 1, 9, 255} {
		if got := NormalizeOrientation(src, orientation); got != image.Image(src) {
			t.Errorf("NormalizeOrientation(_, %d) allocated; want the input returned untouched", orientation)
		}
	}
}

// End to end, the case the whole exif.go file exists for: a portrait photo
// stored landscape with an orientation-6 tag. Go's image/jpeg hands back the
// stored bitmap, so without DecodeNormalized every crop would be a quarter
// turn off.
func TestDecodeNormalizedRotatesAnOrientation6JPEG(t *testing.T) {
	// 40x20, left half red, right half blue. After a 90-degree clockwise
	// rotation the result is 20x40 with the red half on TOP.
	src := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 20 {
				c = color.NRGBA{B: 255, A: 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	data := encodeJPEGWithOrientation(t, src, 6)

	if got := ReadOrientation(data); got != 6 {
		t.Fatalf("ReadOrientation of the spliced JPEG = %d, want 6", got)
	}

	// Without normalisation the stored bitmap is still landscape.
	raw, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if raw.Bounds().Dx() != 40 || raw.Bounds().Dy() != 20 {
		t.Fatalf("Decode gave %v, want 40x20 (image/jpeg must ignore EXIF)", raw.Bounds())
	}

	out, err := DecodeNormalized(data)
	if err != nil {
		t.Fatalf("DecodeNormalized: %v", err)
	}
	if out.Bounds().Dx() != 20 || out.Bounds().Dy() != 40 {
		t.Fatalf("DecodeNormalized gave %v, want 20x40 (the axes must swap)", out.Bounds())
	}

	// Sample well inside each half so JPEG ringing at the seam is irrelevant.
	redder := func(x, y int) bool {
		r, _, b, _ := out.At(x, y).RGBA()
		return r > b
	}
	if !redder(10, 5) {
		t.Error("top half is not red: the rotation went the wrong way")
	}
	if redder(10, 34) {
		t.Error("bottom half is not blue: the rotation went the wrong way")
	}
}

func TestDecodeNormalizedLeavesAnUprightPhotoAlone(t *testing.T) {
	src := solidNRGBA(40, 20, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	out, err := DecodeNormalized(encodeJPEGWithOrientation(t, src, 1))
	if err != nil {
		t.Fatalf("DecodeNormalized: %v", err)
	}
	if out.Bounds().Dx() != 40 || out.Bounds().Dy() != 20 {
		t.Fatalf("bounds = %v, want 40x20", out.Bounds())
	}
}

// --- Downscale ----------------------------------------------------------

// The cap arithmetic at real photo sizes. Split from the resampling because
// CatmullRom over a twelve-megapixel frame costs seconds under -race, and it is
// the arithmetic — the cap, the aspect ratio, the rounding — that is the
// contract. The resampler itself is the documented divergence and is exercised
// end to end below at a size a test can afford.
func TestScaledSizeAppliesTheCap(t *testing.T) {
	cases := []struct {
		name         string
		w, h         int
		maxSide      float64
		wantW, wantH int
		wantScaling  bool
	}{
		{"landscape 12MP to capture cap", 4032, 3024, CaptureMaxSide, 2016, 1512, true},
		{"portrait 12MP to capture cap", 3024, 4032, CaptureMaxSide, 1512, 2016, true},
		{"48MP to capture cap", 8064, 6048, CaptureMaxSide, 2016, 1512, true},
		{"exactly at the capture cap", 2016, 1512, CaptureMaxSide, 2016, 1512, false},
		{"under the capture cap", 1000, 800, CaptureMaxSide, 1000, 800, false},
		{"one pixel over the capture cap", 2017, 2017, CaptureMaxSide, 2016, 2016, true},
		{"capture output to the model cap", 2016, 1512, ModelInputMaxSide, 896, 672, true},
		{"portrait capture to the model cap", 1512, 2016, ModelInputMaxSide, 672, 896, true},
		{"a tile is already under the model cap", 672, 504, ModelInputMaxSide, 672, 504, false},
		{"extreme panorama keeps at least one pixel", 20000, 3, CaptureMaxSide, 2016, 1, true},
		{"degenerate zero size", 0, 0, CaptureMaxSide, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH, gotScaling := scaledSize(c.w, c.h, c.maxSide)
			if gotW != c.wantW || gotH != c.wantH || gotScaling != c.wantScaling {
				t.Fatalf("scaledSize(%d, %d, %v) = (%d, %d, %v), want (%d, %d, %v)",
					c.w, c.h, c.maxSide, gotW, gotH, gotScaling, c.wantW, c.wantH, c.wantScaling)
			}
		})
	}
}

// Target dimensions are ROUNDED, not truncated. 4032x1001 scales by exactly
// 0.5, so the short side lands on 500.5 — truncation would give 500.
func TestScaledSizeRoundsRatherThanTruncates(t *testing.T) {
	gotW, gotH, _ := scaledSize(4032, 1001, CaptureMaxSide)
	if gotW != 2016 || gotH != 501 {
		t.Fatalf("scaledSize(4032, 1001, 2016) = (%d, %d), want (2016, 501); truncation would give 500", gotW, gotH)
	}
	// Swift's .rounded() and Go's math.Round both round half AWAY from zero,
	// so x.5 goes up rather than to even.
	gotW, gotH, _ = scaledSize(4032, 1003, CaptureMaxSide)
	if gotW != 2016 || gotH != 502 {
		t.Fatalf("scaledSize(4032, 1003, 2016) = (%d, %d), want (2016, 502)", gotW, gotH)
	}
}

// End to end through the real resampler, at a size a -race build can afford.
func TestDownscaleCaptureResamples(t *testing.T) {
	src := solidNRGBA(2017, 1008, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	out := DownscaleCapture(src)
	if got := out.Bounds(); got.Dx() != 2016 || got.Dy() != 1008 {
		t.Fatalf("bounds = %v, want 2016x1008", got)
	}
	// A solid source stays solid through any sane filter.
	r, g, b, a := out.At(1000, 500).RGBA()
	if r>>8 != 200 || g>>8 != 100 || b>>8 != 50 || a>>8 != 255 {
		t.Errorf("pixel = (%d,%d,%d,%d), want (200,100,50,255)", r>>8, g>>8, b>>8, a>>8)
	}
}

// "Images already smaller are returned untouched" — no copy, no re-encode, no
// opacity flattening. Callers in Stage 0 rely on the identity.
func TestDownscaleCaptureReturnsSmallImagesUntouched(t *testing.T) {
	for _, size := range [][2]int{{2016, 1512}, {1000, 800}, {1, 1}} {
		src := solidNRGBA(size[0], size[1], color.NRGBA{R: 1, G: 2, B: 3, A: 4})
		if out := DownscaleCapture(src); out != image.Image(src) {
			t.Errorf("DownscaleCapture(%dx%d) allocated; want the input returned untouched", size[0], size[1])
		}
	}
}

func TestDownscaleModelInputCapsAt896(t *testing.T) {
	src := solidNRGBA(1200, 900, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	out := DownscaleModelInput(src)
	if got := out.Bounds(); got.Dx() != 896 || got.Dy() != 672 {
		t.Fatalf("bounds = %v, want 896x672", got)
	}
}

// The trap this pins: in the Swift the tone curve is applied UNCONDITIONALLY
// and only the downscale is behind the size check. Folding the curve inside
// that check would skip it for every crop already under 896 px, which on a
// tiled 2016 px photo is most of them.
func TestDownscaleModelInputAppliesTheToneCurveEvenWhenNoDownscaleIsNeeded(t *testing.T) {
	src := solidNRGBA(100, 80, color.NRGBA{R: 0, G: 64, B: 200, A: 255})
	out := DownscaleModelInput(src)

	if got := out.Bounds(); got.Dx() != 100 || got.Dy() != 80 {
		t.Fatalf("bounds = %v, want 100x80", got)
	}
	r, g, b, a := out.At(50, 40).RGBA()
	if uint8(r>>8) != toneLUT[0] || uint8(g>>8) != toneLUT[64] || uint8(b>>8) != toneLUT[200] {
		t.Errorf("pixel = (%d,%d,%d), want the tone-curved (%d,%d,%d)", r>>8, g>>8, b>>8, toneLUT[0], toneLUT[64], toneLUT[200])
	}
	if a>>8 != 255 {
		t.Errorf("alpha = %d, want 255 (the model input must be opaque)", a>>8)
	}
}

// Order matters: the curve is non-linear, so tone-then-scale and
// scale-then-tone disagree wherever the resampler mixes pixels. Rather than
// predicting CatmullRom's exact output, compute the wrong order and assert the
// two differ.
func TestDownscaleModelInputAppliesTheToneCurveBeforeTheDownscale(t *testing.T) {
	const size = 1024 // over the 896 cap, so the downscale really runs
	src := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		value := byte(0)
		if y >= size/2 {
			value = 255
		}
		row := src.Pix[y*src.Stride : y*src.Stride+size*4]
		copy(row, []byte{value, value, value, 255})
		for filled := 4; filled < len(row); filled *= 2 {
			copy(row[filled:], row[:filled])
		}
	}

	ours := DownscaleModelInput(src)

	// The other order: scale first, tone afterwards.
	scaled, ok := scaleToLongestSide(src, ModelInputMaxSide)
	if !ok {
		t.Fatal("scaleToLongestSide declined to scale a 1792px image")
	}
	wrongOrder := applyToneCurve(scaled)
	makeOpaque(wrongOrder)

	oursNRGBA, ok := ours.(*image.NRGBA)
	if !ok {
		t.Fatalf("DownscaleModelInput returned %T, want *image.NRGBA", ours)
	}
	if oursNRGBA.Bounds() != wrongOrder.Bounds() {
		t.Fatalf("bounds differ: %v vs %v", oursNRGBA.Bounds(), wrongOrder.Bounds())
	}
	if bytes.Equal(oursNRGBA.Pix, wrongOrder.Pix) {
		t.Fatal("tone-then-scale and scale-then-tone produced identical output; the order is not being honoured")
	}
}

func TestMakeOpaqueCompositesOverBlack(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 128})
	img.SetNRGBA(1, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 0})

	makeOpaque(img)

	half := img.NRGBAAt(0, 0)
	if half.A != 255 {
		t.Errorf("alpha = %d, want 255", half.A)
	}
	if half.R != uint8(200*128/255) || half.G != uint8(100*128/255) || half.B != uint8(50*128/255) {
		t.Errorf("half-transparent pixel = %v, want it blended halfway to black", half)
	}
	if clear := img.NRGBAAt(1, 0); clear != (color.NRGBA{A: 255}) {
		t.Errorf("fully transparent pixel = %v, want opaque black", clear)
	}
}

func TestDownscaleModelInputAcceptsEveryDecodedFormat(t *testing.T) {
	data := mustWebP(t)
	img, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	out := DownscaleModelInput(img)
	if got := out.Bounds(); got.Dx() != 10 || got.Dy() != 6 {
		t.Fatalf("bounds = %v, want 10x6", got)
	}
	r, _, _, a := out.At(5, 3).RGBA()
	if uint8(r>>8) != toneLUT[42] {
		t.Errorf("R = %d, want the tone-curved %d", r>>8, toneLUT[42])
	}
	if a>>8 != 255 {
		t.Errorf("alpha = %d, want 255", a>>8)
	}
}
