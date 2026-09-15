package vision

import "encoding/binary"

// OrientationNormal is EXIF orientation 1: the image is already the right way
// up and NormalizeOrientation is a no-op. It is also the value ReadOrientation
// returns for anything it cannot understand.
const OrientationNormal = 1

// exifScanLimit bounds how far into a file ReadOrientation will walk.
//
// 64 KiB. Real cameras and phones write the EXIF APP1 as the first segment
// after SOI, so a legitimate orientation tag is always within the first few
// kilobytes. The bound exists for the other case: uploads arrive over the LAN
// from an unauthenticated-until-paired HTTP endpoint, and a file crafted as a
// long chain of tiny segments would otherwise have us walk the whole thing.
// Bounding the scan turns that into "returns 1 quickly" instead of "returns 1
// eventually".
const exifScanLimit = 64 << 10

// ReadOrientation returns the EXIF Orientation tag (1-8) of a JPEG, or 1.
//
// Hand-rolled on purpose. §1.1 of the implementation plan freezes go.mod, and
// the only thing the whole pipeline needs from EXIF is one 16-bit integer:
// scan the JPEG marker stream for APP1, match the `Exif\0\0` signature, read
// the TIFF header's byte order and IFD0 offset, walk IFD0 for tag 0x0112.
// That is this file. Pulling in a full EXIF library for it would be a
// dependency, a parser, and an attack surface for one number.
//
// It NEVER returns an error and never panics. Every malformation — a truncated
// segment, a byte order that is neither II nor MM, a nonsense IFD offset, a
// value outside 1-8, a PNG, an empty slice — resolves to OrientationNormal,
// which is exactly what a decoder that ignores EXIF would have assumed anyway.
// An unreadable tag must degrade to "assume upright", not to a failed upload.
//
// This matters more on Linux than it did on iOS. UIImage carries orientation
// with it and CropService.normalizedCGImage bakes it in; Go's image/jpeg
// ignores EXIF entirely and hands back the stored, unrotated bitmap. A portrait
// phone photo is stored landscape with a rotation tag, so skipping this step
// does not produce a crash or a sideways preview — it produces a tile grid that
// samples the wrong regions of the photo, and the model dutifully describes
// whatever is there. A silent wrong-answer bug is the worst kind this pipeline
// has, and it is the same class of mistake the Vision coordinate flip is called
// out for in shared/pipeline/scan-pipeline.md § Detection.
func ReadOrientation(data []byte) int {
	// Bound the walk before doing anything else, so every path below is
	// working over at most 64 KiB no matter how large the upload was.
	if len(data) > exifScanLimit {
		data = data[:exifScanLimit]
	}
	// SOI. Anything else (PNG, WebP, HEIC, garbage, empty) is not a JPEG and
	// has no APP1 marker stream to walk.
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return OrientationNormal
	}

	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xFF {
			// The marker stream has desynchronised. Give up rather than
			// hunting for the next plausible-looking 0xFF.
			return OrientationNormal
		}
		marker := data[offset+1]

		// 0xFF is legal padding before a marker byte.
		if marker == 0xFF {
			offset++
			continue
		}
		// Standalone markers carry no length field: SOI, TEM, and the eight
		// restart markers.
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			offset += 2
			continue
		}
		// SOS begins entropy-coded data and EOI ends the image. Either way
		// there are no more APP segments to find.
		if marker == 0xDA || marker == 0xD9 {
			return OrientationNormal
		}

		segLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if segLen < 2 {
			// A length that does not even cover its own field. Malformed,
			// and advancing by it would not terminate.
			return OrientationNormal
		}

		if marker == 0xE1 {
			end := offset + 2 + segLen
			if end > len(data) {
				// The segment claims more bytes than the file has. Read
				// what is actually present rather than following the claim;
				// a truncated APP1 may still hold a complete IFD0.
				end = len(data)
			}
			if orientation, ok := orientationFromAPP1(data[offset+4 : end]); ok {
				return orientation
			}
		}

		// segLen >= 2 guarantees forward progress, so this loop terminates.
		offset += 2 + segLen
	}
	return OrientationNormal
}

// orientationFromAPP1 reads tag 0x0112 out of one APP1 segment's payload.
//
// Reports ok=false for any APP1 that is not EXIF (XMP shares the marker) and
// for every malformation, so the caller keeps scanning later segments instead
// of concluding the file has no orientation.
func orientationFromAPP1(payload []byte) (int, bool) {
	const signature = "Exif\x00\x00"
	if len(payload) < len(signature) || string(payload[:len(signature)]) != signature {
		return 0, false
	}
	// Every offset inside the EXIF block is relative to the start of the TIFF
	// header, not to the file or the segment.
	tiff := payload[len(signature):]
	if len(tiff) < 8 {
		return 0, false
	}

	var order binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		order = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		order = binary.BigEndian
	default:
		return 0, false
	}
	// 42, the TIFF magic. Checking it is what stops a byte-order match by
	// coincidence from being read as a real header.
	if order.Uint16(tiff[2:4]) != 42 {
		return 0, false
	}

	ifdOffset := int(order.Uint32(tiff[4:8]))
	// uint32, so this is the field a crafted file uses to point far outside
	// the data. Reject it rather than indexing with it.
	if ifdOffset < 8 || ifdOffset+2 > len(tiff) {
		return 0, false
	}

	count := int(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	entries := tiff[ifdOffset+2:]
	// Same again for the entry count: 65535 entries claim 786420 bytes, which
	// a 60-byte IFD does not have. Clamp to what is really there.
	if fits := len(entries) / 12; count > fits {
		count = fits
	}

	for i := 0; i < count; i++ {
		entry := entries[i*12 : i*12+12]
		if order.Uint16(entry[0:2]) != 0x0112 {
			continue
		}
		var value int
		switch order.Uint16(entry[2:4]) {
		case 3: // SHORT, the type every real writer uses
			value = int(order.Uint16(entry[8:10]))
		case 4: // LONG, accepted because some writers emit it
			value = int(order.Uint32(entry[8:12]))
		default:
			return 0, false
		}
		if value < 1 || value > 8 {
			return 0, false
		}
		return value, true
	}
	return 0, false
}
