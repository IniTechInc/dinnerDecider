import CoreGraphics
import UIKit
import Vision

/// The crops produced from a source photo, plus the rectangles they came from
/// (in image pixel coordinates, origin top-left) so the UI can show them.
struct CropResult {
    let images: [CGImage]
    let rects: [CGRect]
}

/// Splits a fridge/pantry photo into per-item crops before identification.
///
/// Following the spec's "crop, then ask" insight: vision models collapse on
/// small subjects, so we detect salient objects and rectangles, merge and pad
/// their boxes, and crop each one. If detection is weak (fewer than 2 boxes) we
/// fall back to a 3x3 overlapping tile grid so nothing is missed.
enum CropService {

    private static let paddingFraction: CGFloat = 0.15
    private static let tileOverlap: CGFloat = 0.20

    static func crops(from image: UIImage) -> CropResult {
        // Normalize orientation and handle CIImage-backed UIImages (e.g. HDR photos from
        // PhotosPicker). UIImage(data:) for HEIC/HDR often has cgImage == nil, which would
        // silently return zero crops. Drawing through UIGraphicsImageRenderer flattens
        // both the color space and the EXIF rotation into a plain up-oriented CGImage.
        guard let cgImage = normalizedCGImage(from: image) else {
            return CropResult(images: [], rects: [])
        }
        let width = cgImage.width
        let height = cgImage.height

        var boxes = detectBoxes(in: cgImage, width: width, height: height)
        boxes = boxes.map { pad(rect: $0, width: width, height: height) }

        if boxes.count < 2 {
            boxes = tileGrid(width: width, height: height)
        }

        var images: [CGImage] = []
        var rects: [CGRect] = []
        for box in boxes {
            let integral = box.integral
            if integral.width >= 8, integral.height >= 8, let cropped = cgImage.cropping(to: integral) {
                images.append(cropped)
                rects.append(integral)
            }
        }
        return CropResult(images: images, rects: rects)
    }

    // MARK: - Detection

    private static func detectBoxes(in cgImage: CGImage, width: Int, height: Int) -> [CGRect] {
        // Image is already orientation-normalized via normalizedCGImage, so .up is correct.
        let handler = VNImageRequestHandler(cgImage: cgImage, orientation: .up, options: [:])

        let saliency = VNGenerateObjectnessBasedSaliencyImageRequest()
        let rectangles = VNDetectRectanglesRequest()
        rectangles.maximumObservations = 8
        rectangles.minimumConfidence = 0.15
        rectangles.minimumSize = 0.05

        try? handler.perform([saliency, rectangles])

        var boxes: [CGRect] = []
        if let observation = saliency.results?.first, let objects = observation.salientObjects {
            for object in objects {
                boxes.append(pixelRect(object.boundingBox, width: width, height: height))
            }
        }
        if let results = rectangles.results {
            for result in results {
                boxes.append(pixelRect(result.boundingBox, width: width, height: height))
            }
        }
        return mergeOverlapping(boxes)
    }

    // MARK: - Geometry helpers

    /// Convert a Vision normalized rect (origin bottom-left) to pixel coordinates
    /// with origin top-left, matching CGImage's coordinate space.
    private static func pixelRect(_ normalized: CGRect, width: Int, height: Int) -> CGRect {
        let w = CGFloat(width)
        let h = CGFloat(height)
        let x = normalized.minX * w
        let y = (1 - normalized.maxY) * h
        return CGRect(x: x, y: y, width: normalized.width * w, height: normalized.height * h)
    }

    private static func pad(rect: CGRect, width: Int, height: Int) -> CGRect {
        let dx = rect.width * paddingFraction
        let dy = rect.height * paddingFraction
        let padded = rect.insetBy(dx: -dx, dy: -dy)
        let bounds = CGRect(x: 0, y: 0, width: CGFloat(width), height: CGFloat(height))
        return padded.intersection(bounds)
    }

    /// Greedily merge boxes whose intersection-over-union exceeds a threshold.
    private static func mergeOverlapping(_ boxes: [CGRect]) -> [CGRect] {
        var merged: [CGRect] = []
        for box in boxes where box.width > 0 && box.height > 0 {
            var didMerge = false
            for index in merged.indices {
                if iou(merged[index], box) > 0.3 {
                    merged[index] = merged[index].union(box)
                    didMerge = true
                    break
                }
            }
            if !didMerge {
                merged.append(box)
            }
        }
        return merged
    }

    private static func iou(_ a: CGRect, _ b: CGRect) -> CGFloat {
        let intersection = a.intersection(b)
        if intersection.isNull { return 0 }
        let intersectionArea = intersection.width * intersection.height
        let unionArea = a.width * a.height + b.width * b.height - intersectionArea
        return unionArea > 0 ? intersectionArea / unionArea : 0
    }

    /// Returns a CGImage that is guaranteed to be in the `.up` orientation and
    /// has pixel data (not a deferred CIImage). This handles two failure modes:
    ///
    /// 1. HDR / HEIC photos from PhotosPicker: `UIImage(data:)` may produce a
    ///    CIImage-backed UIImage where `.cgImage` is nil.
    /// 2. Portrait iPhone photos: the raw CGImage is landscape with an EXIF
    ///    rotation tag stored in `imageOrientation`. Without normalization,
    ///    Vision detects boxes in the wrong coordinate frame.
    static func normalizedCGImage(from image: UIImage) -> CGImage? {
        if image.imageOrientation == .up, let cg = image.cgImage { return cg }
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = false
        let renderer = UIGraphicsImageRenderer(size: image.size, format: format)
        return renderer.image { _ in
            image.draw(in: CGRect(origin: .zero, size: image.size))
        }.cgImage
    }

    /// A 3x3 grid of overlapping tiles covering the whole image.
    private static func tileGrid(width: Int, height: Int) -> [CGRect] {
        let tileWidth = CGFloat(width) / 3.0
        let tileHeight = CGFloat(height) / 3.0
        let stepX = tileWidth * (1 - tileOverlap)
        let stepY = tileHeight * (1 - tileOverlap)

        var rects: [CGRect] = []
        var y: CGFloat = 0
        while y < CGFloat(height) {
            var x: CGFloat = 0
            while x < CGFloat(width) {
                let w = min(tileWidth, CGFloat(width) - x)
                let h = min(tileHeight, CGFloat(height) - y)
                if w > 8, h > 8 {
                    rects.append(CGRect(x: x, y: y, width: w, height: h))
                }
                x += stepX
            }
            y += stepY
        }
        return rects
    }
}
