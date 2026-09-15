package vision

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"

	_ "image/jpeg" // registers the JPEG decoder with image.Decode
	_ "image/png"  // registers the PNG decoder with image.Decode

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder with image.Decode
)

// CaptureMaxSide is the longest-side cap applied to every photo entering the
// service, from `ImageDownscaler.captureMaxSide`.
//
// 2016 px. On iOS this was a wired-memory decision: a 24 MP frame is ~90 MB
// decoded and a batch stages up to eight of them, on top of a runtime already
// holding ~5 GB. On a 8 GiB Pi with a 4-5 GiB working set the arithmetic is the
// same one, so the number carries over unchanged. Crops are re-scaled to 896 px
// for the model anyway, so this costs nothing in crop quality.
const CaptureMaxSide = 2016

// ModelInputMaxSide is the longest-side cap applied immediately before
// inference, from `GemmaLLMService.maxImageSide`.
const ModelInputMaxSide = 896

// ErrUnsupportedFormat is returned by Decode for an image format this port
// deliberately does not decode.
//
// Wrapped with the detected format name, so the API layer can turn it into an
// actionable message rather than "upload failed": HEIC is transcoded in the
// browser on a canvas before upload (implementation plan §1.1), and TIFF is not
// an accepted upload format at all. Both decisions exist because the
// alternatives are cgo — libheif — or a dependency on a frozen go.mod. Test
// with errors.Is(err, ErrUnsupportedFormat).
var ErrUnsupportedFormat = errors.New("unsupported image format")

// DetectFormat sniffs the container format from the leading bytes.
//
// Returns "jpeg", "png" or "webp" for what this port decodes, the name of the
// format for what it recognises but refuses ("heic", "tiff", "gif", "bmp"), or
// "" for anything unrecognised. Sniffing rather than trusting a Content-Type
// header or a file extension, because both are caller-supplied.
func DetectFormat(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "gif"
	case len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*"):
		return "tiff"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "bmp"
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		// ISO base media file. The HEIF family is what a modern iPhone or
		// Android camera produces by default, so this branch is hit often
		// and its message matters.
		switch string(data[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevm", "hevs", "mif1", "msf1":
			return "heic"
		case "avif", "avis":
			return "avif"
		}
		return "iso-media"
	}
	return ""
}

// Decode decodes JPEG, PNG and WebP. Everything else is ErrUnsupportedFormat.
//
// No HEIC and no TIFF, by decision rather than oversight: HEIC needs libheif
// (cgo, and CGO_ENABLED=0 is a hard build gate) and TIFF is not an accepted
// upload format. Both are rejected BY NAME so the API layer can say which one
// it was and what to do about it — an unnamed "could not read image" on a
// folder of iPhone photos is a dead end for the user.
//
// Decode does NOT apply the orientation tag; the orientation lives in the file
// bytes, not in the decoded image, so it has to be read separately. Use
// DecodeNormalized unless you have a reason not to.
func Decode(data []byte) (image.Image, error) {
	switch format := DetectFormat(data); format {
	case "jpeg", "png", "webp":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", format, err)
		}
		return img, nil
	case "":
		return nil, fmt.Errorf("%w: unrecognised image data", ErrUnsupportedFormat)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
	}
}

// DecodeNormalized decodes and bakes in the EXIF orientation.
//
// This pairing is the whole point of exif.go and is easy to split by accident:
// Go's image/jpeg ignores EXIF, so Decode alone hands back a portrait photo
// stored landscape. Everything downstream — the tile grid, the crops, the
// model — then works on a frame rotated a quarter turn from what the user
// photographed. This is the port's equivalent of
// `CropService.normalizedCGImage`, minus the pixel-backing half, which Go does
// not need because every decoder here returns a materialised bitmap.
func DecodeNormalized(data []byte) (image.Image, error) {
	img, err := Decode(data)
	if err != nil {
		return nil, err
	}
	return NormalizeOrientation(img, ReadOrientation(data)), nil
}

// NormalizeOrientation returns img rotated and flipped so it is upright.
//
// orientation is an EXIF Orientation value from ReadOrientation. 1 (and any
// value outside 1-8) returns img untouched; the other seven allocate a new
// bitmap. Orientations 5-8 transpose the axes, so the result's width and height
// are swapped.
//
// Source pixel for each destination pixel, which is the inverse of the usual
// table and the direction that avoids gaps:
//
//	2  horizontal flip        (w-1-dx, dy)
//	3  180 degrees            (w-1-dx, h-1-dy)
//	4  vertical flip          (dx,     h-1-dy)
//	5  transpose              (dy,     dx)
//	6  90 clockwise           (dy,     h-1-dx)   <- the common portrait case
//	7  transverse             (w-1-dy, h-1-dx)
//	8  90 counter-clockwise   (w-1-dy, dx)
func NormalizeOrientation(img image.Image, orientation int) image.Image {
	if orientation <= OrientationNormal || orientation > 8 {
		return img
	}
	src := toNRGBA(img)
	w := src.Rect.Dx()
	h := src.Rect.Dy()

	// sx = ax*dx + bx*dy + cx, sy = ay*dx + by*dy + cy. Hoisted out of the
	// pixel loop; a 2016x1512 frame is three million iterations.
	var ax, bx, cx, ay, by, cy int
	transposes := orientation >= 5
	switch orientation {
	case 2:
		ax, bx, cx, ay, by, cy = -1, 0, w-1, 0, 1, 0
	case 3:
		ax, bx, cx, ay, by, cy = -1, 0, w-1, 0, -1, h-1
	case 4:
		ax, bx, cx, ay, by, cy = 1, 0, 0, 0, -1, h-1
	case 5:
		ax, bx, cx, ay, by, cy = 0, 1, 0, 1, 0, 0
	case 6:
		ax, bx, cx, ay, by, cy = 0, 1, 0, -1, 0, h-1
	case 7:
		ax, bx, cx, ay, by, cy = 0, -1, w-1, -1, 0, h-1
	case 8:
		ax, bx, cx, ay, by, cy = 0, -1, w-1, 1, 0, 0
	}

	dw, dh := w, h
	if transposes {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		for dx := 0; dx < dw; dx++ {
			si := src.PixOffset(ax*dx+bx*dy+cx+src.Rect.Min.X, ay*dx+by*dy+cy+src.Rect.Min.Y)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// DownscaleCapture caps the longest side at CaptureMaxSide.
//
// Port of `ImageDownscaler.forCapture`. An image already small enough is
// returned UNTOUCHED, exactly as the Swift does — no re-encode, no copy, no
// opacity flattening. Stage 0 of shared/pipeline/scan-pipeline.md says
// "Images already smaller are returned untouched" and callers rely on it.
func DownscaleCapture(img image.Image) image.Image {
	if scaled, ok := scaleToLongestSide(img, CaptureMaxSide); ok {
		return scaled
	}
	return img
}

// DownscaleModelInput prepares an image for inference: tone curve, then
// downscale to ModelInputMaxSide, then flatten to opaque.
//
// Port of `GemmaLLMService.downscaled`, and the order is the Swift's: the tone
// curve is applied UNCONDITIONALLY, the downscale only when the image exceeds
// the cap. Folding the curve inside the size check would silently skip it for
// every crop already under 896 px, which on a tiled 2016 px photo is most of
// them.
//
// Unlike DownscaleCapture this always returns a fresh, opaque bitmap: it is the
// last thing that happens before the bytes are encoded for the model, and an
// image with an alpha channel at that boundary is undefined rather than useful.
func DownscaleModelInput(img image.Image) image.Image {
	out := applyToneCurve(img)
	if scaled, ok := scaleToLongestSide(out, ModelInputMaxSide); ok {
		out = scaled
	}
	makeOpaque(out)
	return out
}

// scaleToLongestSide resamples img so its longest side is at most maxSide,
// reporting false (and doing no work) when it already is.
//
// ⚠️ Honest divergence, the second of two in this file. iOS resamples through
// Core Graphics inside a UIGraphicsImageRenderer; this uses
// x/image/draw.CatmullRom. Both are bicubic-family filters over the same
// pixels, but their kernels, edge handling and intermediate precision differ,
// so the output is NOT byte-identical and cannot be made so without
// reimplementing Core Graphics. CatmullRom is the closest available match and
// is what the implementation plan pins. Parity here is validated by RECALL in
// eval/, not by pixel equality.
//
// Target dimensions are rounded (Swift's `.rounded()` is round-half-away-from-
// zero, and so is math.Round), and clamped to at least one pixel.
func scaleToLongestSide(img image.Image, maxSide float64) (*image.NRGBA, bool) {
	bounds := img.Bounds()
	targetW, targetH, needed := scaledSize(bounds.Dx(), bounds.Dy(), maxSide)
	if !needed {
		return nil, false
	}
	dst := image.NewNRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, xdraw.Src, nil)
	return dst, true
}

// scaledSize is the dimension arithmetic of a longest-side cap, split out from
// the resampling so it can be tested at real photo sizes without resampling a
// twelve-megapixel bitmap.
//
// Reports needed=false when the image is already within the cap, which is what
// makes DownscaleCapture return its input untouched.
func scaledSize(w, h int, maxSide float64) (int, int, bool) {
	fw := float64(w)
	fh := float64(h)
	longest := math.Max(fw, fh)
	if longest <= maxSide || longest <= 0 {
		return w, h, false
	}

	scale := maxSide / longest
	targetW := int(math.Round(fw * scale))
	targetH := int(math.Round(fh * scale))
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}
	return targetW, targetH, true
}

// makeOpaque composites an image onto black in place and sets alpha to 255.
//
// Matches a UIGraphicsImageRendererFormat with `opaque = true`, whose backing
// store starts black and which anything semi-transparent is blended into. In
// practice every photo that reaches here is already opaque (JPEG and WebP-lossy
// have no alpha), so this only bites on a PNG with transparency — but leaving
// alpha in the buffer handed to the model is undefined behaviour, not a
// nicety.
func makeOpaque(img *image.NRGBA) {
	pix := img.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		if a := pix[i+3]; a != 0xFF {
			pix[i+0] = uint8(int(pix[i+0]) * int(a) / 255)
			pix[i+1] = uint8(int(pix[i+1]) * int(a) / 255)
			pix[i+2] = uint8(int(pix[i+2]) * int(a) / 255)
			pix[i+3] = 0xFF
		}
	}
}

// toNRGBA returns img as a zero-origin, straight-alpha NRGBA bitmap, without
// copying when it already is one.
//
// Straight alpha rather than premultiplied because the tone curve is a
// per-channel colour transform, and applying one to premultiplied values is
// wrong wherever alpha is not 255.
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	bounds := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	return dst
}

// toneCurveControlPoints are the five CIToneCurve control points, verbatim from
// `GemmaLLMService.downscaled` and the table in
// shared/pipeline/scan-pipeline.md § Stage 4.
//
// A gentle lift of the shadows and midtones with the white point pinned, for
// dim fridge and pantry photos — the single most common thing a user
// photographs badly.
var toneCurveControlPoints = [5][2]float64{
	{0.00, 0.05},
	{0.25, 0.28},
	{0.50, 0.55},
	{0.75, 0.78},
	{1.00, 1.00},
}

// toneLUT is the 256-entry per-channel tone curve, built ONCE at package init.
//
// This is the Go analogue of the shipped rule that the image-processing context
// is created once for the app's lifetime and never per crop. On iOS the reason
// was blunt: "Creating one per-crop would allocate a new Metal GPU context each
// time, exhausting wired memory on top of the already ~3.5GB GPU-resident model
// and triggering jetsam" (GemmaLLMService.swift). Nothing here allocates a GPU
// context, but the shape of the mistake is identical — a scan runs this over
// four full-size images, and rebuilding a spline per call is work that belongs
// at init. A package-level array is the whole mechanism.
var toneLUT = buildToneCurveLUT()

// buildToneCurveLUT interpolates the five control points into 256 8-bit
// entries using monotone cubic Hermite (Fritsch-Carlson) interpolation.
//
// ⚠️ HONEST DIVERGENCE. Byte-exact parity with Core Image is NOT ACHIEVABLE and
// is not attempted. `CIToneCurve` fits a spline and evaluates it in Core
// Image's own working colour space, at its own precision, with its own
// handling of the segments outside the control points — none of which is
// specified, portable, or reproducible from outside the framework. What is
// reproducible is the SHAPE: five fixed control points, monotone, white pinned
// at 1.0, black lifted to 0.05. This LUT reproduces that shape on 8-bit sRGB.
//
// Fritsch-Carlson specifically, rather than a natural cubic spline, because a
// natural spline through these points overshoots: it would dip below the
// previous value somewhere in the 0.25-0.5 segment, and a tone curve that is
// not monotonic inverts contrast in a band of the image. Monotonicity is the
// property that has to hold; smoothness is only nice to have.
//
// Consequence for anyone tuning this: the correctness test for the curve is
// RECALL IN eval/, not pixel equality with an iOS capture. Changing the
// interpolation method — or the control points — is an eval-harness change,
// not a refactor.
func buildToneCurveLUT() [256]uint8 {
	const n = len(toneCurveControlPoints)

	var xs, ys [n]float64
	for i, point := range toneCurveControlPoints {
		xs[i], ys[i] = point[0], point[1]
	}

	// Secant slopes between consecutive points.
	var delta [n - 1]float64
	for i := 0; i < n-1; i++ {
		delta[i] = (ys[i+1] - ys[i]) / (xs[i+1] - xs[i])
	}

	// Initial tangents: one-sided at the ends, averaged in the interior.
	var m [n]float64
	m[0] = delta[0]
	m[n-1] = delta[n-2]
	for i := 1; i < n-1; i++ {
		m[i] = (delta[i-1] + delta[i]) / 2
	}

	// The Fritsch-Carlson filter: clamp any tangent pair that would let the
	// cubic overshoot its segment, which is what guarantees monotonicity.
	for i := 0; i < n-1; i++ {
		if delta[i] == 0 {
			m[i], m[i+1] = 0, 0
			continue
		}
		alpha := m[i] / delta[i]
		beta := m[i+1] / delta[i]
		if sum := alpha*alpha + beta*beta; sum > 9 {
			tau := 3 / math.Sqrt(sum)
			m[i] = tau * alpha * delta[i]
			m[i+1] = tau * beta * delta[i]
		}
	}

	var lut [256]uint8
	for i := 0; i < 256; i++ {
		x := float64(i) / 255

		segment := 0
		for segment < n-2 && x > xs[segment+1] {
			segment++
		}
		h := xs[segment+1] - xs[segment]
		t := (x - xs[segment]) / h
		t2 := t * t
		t3 := t2 * t

		// Cubic Hermite basis.
		y := (2*t3-3*t2+1)*ys[segment] +
			(t3-2*t2+t)*h*m[segment] +
			(-2*t3+3*t2)*ys[segment+1] +
			(t3-t2)*h*m[segment+1]

		scaled := math.Round(y * 255)
		if scaled < 0 {
			scaled = 0
		}
		if scaled > 255 {
			scaled = 255
		}
		lut[i] = uint8(scaled)
	}
	return lut
}

// applyToneCurve maps every colour channel through toneLUT, leaving alpha
// alone, and returns a new bitmap. The caller's image is never modified.
func applyToneCurve(img image.Image) *image.NRGBA {
	dst := toNRGBA(img)
	// toNRGBA hands back the caller's own bitmap when it is already a
	// zero-origin NRGBA, and this function must not write into it.
	if original, ok := img.(*image.NRGBA); ok && original == dst {
		dst = image.NewNRGBA(original.Rect)
		copy(dst.Pix, original.Pix)
	}

	pix := dst.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		pix[i+0] = toneLUT[pix[i+0]]
		pix[i+1] = toneLUT[pix[i+1]]
		pix[i+2] = toneLUT[pix[i+2]]
	}
	return dst
}
