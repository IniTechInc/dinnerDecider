package vision

import (
	"encoding/binary"
	"math/rand"
	"testing"
)

// byteOrder is binary.ByteOrder plus the Append helpers, both of which
// binary.LittleEndian and binary.BigEndian satisfy.
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// exifSpec builds a TIFF/EXIF block field by field so a test can corrupt
// exactly one thing and leave the rest valid.
type exifSpec struct {
	bigEndian   bool
	magic       uint16
	ifdOffset   uint32
	entryCount  uint16
	tag         uint16
	typ         uint16
	orientation uint32
}

func validExif(orientation uint32) exifSpec {
	return exifSpec{magic: 42, ifdOffset: 8, entryCount: 1, tag: 0x0112, typ: 3, orientation: orientation}
}

func (s exifSpec) tiff() []byte {
	var order byteOrder = binary.LittleEndian
	buf := []byte("II")
	if s.bigEndian {
		order = binary.BigEndian
		buf = []byte("MM")
	}
	buf = order.AppendUint16(buf, s.magic)
	buf = order.AppendUint32(buf, s.ifdOffset)
	// Pad to a plausible IFD offset, but never chase an absurd one — that is
	// the whole point of the bounds tests below.
	for len(buf) < int(s.ifdOffset) && len(buf) < 1024 {
		buf = append(buf, 0)
	}
	buf = order.AppendUint16(buf, s.entryCount)
	buf = order.AppendUint16(buf, s.tag)
	buf = order.AppendUint16(buf, s.typ)
	buf = order.AppendUint32(buf, 1) // component count
	if s.typ == 3 {                  // SHORT: value in the first 2 of 4 bytes
		buf = order.AppendUint16(buf, uint16(s.orientation))
		buf = order.AppendUint16(buf, 0)
	} else {
		buf = order.AppendUint32(buf, s.orientation)
	}
	buf = order.AppendUint32(buf, 0) // next-IFD offset
	return buf
}

// segment wraps a payload in a JPEG marker segment with its 16-bit length.
func segment(marker byte, payload []byte) []byte {
	out := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(out[2:4], uint16(len(payload)+2))
	return append(out, payload...)
}

func exifSegment(tiff []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), tiff...)
	return segment(0xE1, payload)
}

// jpegWith assembles SOI + the given segments + SOS + EOI.
func jpegWith(segments ...[]byte) []byte {
	out := []byte{0xFF, 0xD8}
	for _, s := range segments {
		out = append(out, s...)
	}
	return append(out, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9)
}

func exifJPEG(orientation uint32, bigEndian bool) []byte {
	spec := validExif(orientation)
	spec.bigEndian = bigEndian
	return jpegWith(exifSegment(spec.tiff()))
}

// All eight legal values, in both TIFF byte orders. A phone writes 6 for a
// portrait photo held upright, which is the case the whole file exists for.
func TestReadOrientationAllEightValuesBothByteOrders(t *testing.T) {
	for want := 1; want <= 8; want++ {
		for _, bigEndian := range []bool{false, true} {
			order := "II (little-endian)"
			if bigEndian {
				order = "MM (big-endian)"
			}
			got := ReadOrientation(exifJPEG(uint32(want), bigEndian))
			if got != want {
				t.Errorf("ReadOrientation(orientation %d, %s) = %d, want %d", want, order, got, want)
			}
		}
	}
}

// Some writers store the tag as LONG rather than SHORT.
func TestReadOrientationAcceptsLongTypedTag(t *testing.T) {
	spec := validExif(6)
	spec.typ = 4
	if got := ReadOrientation(jpegWith(exifSegment(spec.tiff()))); got != 6 {
		t.Fatalf("LONG-typed orientation = %d, want 6", got)
	}
}

func TestReadOrientationDefaultsToNormal(t *testing.T) {
	// A PNG header, a WebP header, an empty slice and a bare SOI must all
	// resolve to 1 rather than erroring: the pipeline decodes these formats,
	// they simply carry no JPEG marker stream.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)

	cases := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"one byte", []byte{0xFF}},
		{"bare SOI", []byte{0xFF, 0xD8}},
		{"PNG", png},
		{"WebP", webp},
		{"not an image at all", []byte("this is a shopping list, not a photo")},
		{"JPEG with no APP1", jpegWith(segment(0xDB, make([]byte, 64)))},
		{"JPEG whose marker stream desynchronises", append([]byte{0xFF, 0xD8}, make([]byte, 64)...)},
		{"APP1 that is XMP, not EXIF", jpegWith(segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReadOrientation(c.data); got != OrientationNormal {
				t.Fatalf("ReadOrientation(%s) = %d, want 1", c.name, got)
			}
		})
	}
}

// A non-EXIF APP1 before the real one must be stepped over, not treated as the
// answer. Phones routinely write XMP alongside EXIF.
func TestReadOrientationSkipsNonExifAPP1AndKeepsLooking(t *testing.T) {
	xmp := segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"))
	data := jpegWith(xmp, exifSegment(validExif(8).tiff()))
	if got := ReadOrientation(data); got != 8 {
		t.Fatalf("orientation after an XMP APP1 = %d, want 8", got)
	}
}

// Entropy-coded scan data is full of bytes that look like markers. Stopping at
// SOS is what keeps the walk from wandering into it.
func TestReadOrientationStopsAtStartOfScan(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02}
	data = append(data, exifSegment(validExif(6).tiff())...)
	if got := ReadOrientation(data); got != OrientationNormal {
		t.Fatalf("orientation found after SOS = %d, want 1 (the walk must stop at SOS)", got)
	}
}

func TestReadOrientationRejectsMalformedTIFF(t *testing.T) {
	badOrder := validExif(6)
	badOrder.magic = 42
	badOrderTIFF := badOrder.tiff()
	badOrderTIFF[0], badOrderTIFF[1] = 'X', 'Y'

	badMagic := validExif(6)
	badMagic.magic = 43

	shortOffset := validExif(6)
	shortOffset.ifdOffset = 2 // inside the TIFF header itself

	badType := validExif(6)
	badType.typ = 5 // RATIONAL, never used for Orientation

	cases := []struct {
		name string
		tiff []byte
	}{
		{"byte order is neither II nor MM", badOrderTIFF},
		{"TIFF magic is not 42", badMagic.tiff()},
		{"IFD offset points inside the header", shortOffset.tiff()},
		{"tag type is not SHORT or LONG", badType.tiff()},
		{"TIFF block is too short for a header", []byte("II*\x00")},
		{"TIFF block is empty", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReadOrientation(jpegWith(exifSegment(c.tiff))); got != OrientationNormal {
				t.Fatalf("ReadOrientation(%s) = %d, want 1", c.name, got)
			}
		})
	}
}

// 0 and 9 are outside the legal 1-8 range. Returning them would index off the
// end of NormalizeOrientation's transform table.
func TestReadOrientationRejectsOutOfRangeValues(t *testing.T) {
	for _, value := range []uint32{0, 9, 255, 65535} {
		if got := ReadOrientation(jpegWith(exifSegment(validExif(value).tiff()))); got != OrientationNormal {
			t.Errorf("ReadOrientation(orientation field %d) = %d, want 1", value, got)
		}
	}
}

// A file cut off mid-IFD must resolve to 1, not to a half-read entry.
func TestReadOrientationHandlesTruncatedAPP1(t *testing.T) {
	full := jpegWith(exifSegment(validExif(6).tiff()))
	// Cut inside the single IFD entry: header (2) + APP1 marker/len (4) +
	// "Exif\0\0" (6) + TIFF header (8) + entry count (2) = 22 bytes of
	// preamble, then the 12-byte entry. Stop six bytes into it.
	truncated := full[:28]
	if got := ReadOrientation(truncated); got != OrientationNormal {
		t.Fatalf("truncated APP1 = %d, want 1", got)
	}
}

// Deliberate lenience, stated so it is not mistaken for a bug: a segment whose
// declared length runs past the end of the file still has its PRESENT bytes
// read. Phones and photo editors do emit files whose last segment was clipped,
// and the IFD is at the front of the segment. What we must never do is trust
// the declared length as a bound to walk to.
func TestReadOrientationReadsPresentBytesWithoutFollowingAClaimedLength(t *testing.T) {
	tiff := validExif(3).tiff()
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0xFF, 0xFF} // declares 65535 bytes
	seg = append(seg, payload...)
	data := append([]byte{0xFF, 0xD8}, seg...) // and then the file just ends

	if got := ReadOrientation(data); got != 3 {
		t.Fatalf("orientation from an over-declared APP1 = %d, want 3", got)
	}
}

// The crafted-length cases. A JPEG segment length field is 16 bits, so it can
// never literally say "100 MB" — the two places a file CAN claim a size that
// large are the TIFF IFD0 offset (uint32) and the IFD entry count (uint16,
// which claims 65535*12 = 786420 bytes of entries). Both must be bounded
// against the bytes actually present rather than followed.
func TestReadOrientationBoundsCraftedLengths(t *testing.T) {
	hundredMB := validExif(6)
	hundredMB.ifdOffset = 100 << 20 // 104857600

	maxOffset := validExif(6)
	maxOffset.ifdOffset = 0xFFFFFFFF

	cases := []struct {
		name string
		spec exifSpec
	}{
		{"IFD0 offset claims 100 MB", hundredMB},
		{"IFD0 offset claims 4 GiB", maxOffset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReadOrientation(jpegWith(exifSegment(c.spec.tiff()))); got != OrientationNormal {
				t.Fatalf("ReadOrientation(%s) = %d, want 1", c.name, got)
			}
		})
	}

	// The entry count is CLAMPED rather than rejected, which is the right
	// behavior and worth pinning so nobody "hardens" it into a rejection: the
	// count claims 786420 bytes of entries, the IFD holds one, and the one
	// that is really there is still read. Bounded, not discarded.
	t.Run("IFD claiming 65535 entries is clamped to the entries present", func(t *testing.T) {
		hugeCount := validExif(6)
		hugeCount.entryCount = 65535
		if got := ReadOrientation(jpegWith(exifSegment(hugeCount.tiff()))); got != 6 {
			t.Fatalf("ReadOrientation(65535 claimed entries) = %d, want 6", got)
		}
	})

	// The same claim with NO entry actually present must fall back to 1
	// rather than reading past the end of the IFD.
	t.Run("IFD claiming 65535 entries with none present", func(t *testing.T) {
		spec := validExif(6)
		spec.entryCount = 65535
		tiff := spec.tiff()
		if got := ReadOrientation(jpegWith(exifSegment(tiff[:10]))); got != OrientationNormal {
			t.Fatalf("ReadOrientation(65535 claimed, 0 present) = %d, want 1", got)
		}
	})
}

// The 64 KiB scan bound, asserted by observing what it costs: an EXIF block
// sitting past the bound is not found, while the same block inside it is.
func TestReadOrientationScanIsBoundedTo64KiB(t *testing.T) {
	// A COM segment: 8002 bytes on the wire.
	filler := func() []byte { return segment(0xFE, make([]byte, 8000)) }

	build := func(count int) []byte {
		segments := make([][]byte, 0, count+1)
		for i := 0; i < count; i++ {
			segments = append(segments, filler())
		}
		segments = append(segments, exifSegment(validExif(7).tiff()))
		return jpegWith(segments...)
	}

	within := build(4) // ~32 KiB of filler before the APP1
	if got := ReadOrientation(within); got != 7 {
		t.Fatalf("EXIF at ~32 KiB = %d, want 7 (the control case must be findable)", got)
	}

	beyond := build(9) // ~72 KiB of filler, past the bound
	if len(beyond) <= exifScanLimit {
		t.Fatalf("test is not exercising the bound: file is only %d bytes", len(beyond))
	}
	if got := ReadOrientation(beyond); got != OrientationNormal {
		t.Fatalf("EXIF past the 64 KiB bound = %d, want 1 (the scan must stop)", got)
	}

	// A megabyte of nothing but filler must also terminate at 1.
	big := make([]byte, 0, 1<<20)
	big = append(big, 0xFF, 0xD8)
	for len(big) < 1<<20 {
		big = append(big, filler()...)
	}
	if got := ReadOrientation(big); got != OrientationNormal {
		t.Fatalf("1 MB of filler = %d, want 1", got)
	}
}

// ReadOrientation runs on bytes that arrived over the network. Every prefix of
// a valid file, and a pile of random garbage, must return a legal value and
// must not panic.
func TestReadOrientationNeverPanicsAndAlwaysReturnsALegalValue(t *testing.T) {
	valid := jpegWith(
		segment(0xE0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00")),
		exifSegment(validExif(6).tiff()),
		segment(0xDB, make([]byte, 64)),
	)
	for i := 0; i <= len(valid); i++ {
		got := ReadOrientation(valid[:i])
		if got < 1 || got > 8 {
			t.Fatalf("ReadOrientation(prefix of length %d) = %d, outside 1-8", i, got)
		}
	}

	rng := rand.New(rand.NewSource(20260915))
	for i := 0; i < 500; i++ {
		buf := make([]byte, 2+rng.Intn(4096))
		rng.Read(buf)
		if i%2 == 0 { // half of them look like JPEGs
			buf[0], buf[1] = 0xFF, 0xD8
		}
		got := ReadOrientation(buf)
		if got < 1 || got > 8 {
			t.Fatalf("ReadOrientation(random buffer %d) = %d, outside 1-8", i, got)
		}
	}
}
