import UIKit

/// Shrinks photos as they enter the app so we never hold full-resolution frames
/// in memory longer than the instant it takes to decode them.
///
/// Why this matters for stability: on device the Gemma runtime holds ~5GB of
/// WIRED Metal memory (weights + vision projector + KV cache). A `vm-pageshortage`
/// jetsam killed the app not because of its own footprint but because the system
/// ran out of *pageable* memory. Every megabyte of pixel data the app is not
/// holding is a megabyte the system can keep free. A 24MP Pro Max frame is ~90MB
/// decoded; up to 8 of those staged for a scan is a lot of avoidable pressure.
/// Crops are re-scaled to 896px for the model anyway, so retaining ~2000px
/// originals keeps crop quality while cutting resident pixel memory roughly 10x.
enum ImageDownscaler {

    /// Longest-side cap for photos retained through the capture/scan pipeline.
    static let captureMaxSide: CGFloat = 2016

    /// Downscale `image` so its longest side is at most `maxSide`. Returns the
    /// original untouched when it is already small enough. The result is baked to
    /// `.up` orientation and opaque at scale 1, so it is a plain, compact bitmap.
    static func downscaled(_ image: UIImage, maxSide: CGFloat) -> UIImage {
        let size = image.size
        let longest = max(size.width, size.height)
        guard longest > maxSide, longest > 0 else { return image }

        let scale = maxSide / longest
        let target = CGSize(width: (size.width * scale).rounded(), height: (size.height * scale).rounded())
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = true
        let renderer = UIGraphicsImageRenderer(size: target, format: format)
        return renderer.image { _ in
            image.draw(in: CGRect(origin: .zero, size: target))
        }
    }

    /// Downscale a freshly captured or imported photo to the capture cap.
    static func forCapture(_ image: UIImage) -> UIImage {
        downscaled(image, maxSide: captureMaxSide)
    }
}
