import UIKit

/// A single photo staged for a scan, carrying a stable identity.
///
/// Wrapping each `UIImage` in an `Identifiable` value is the whole point: it lets
/// SwiftUI's `ForEach` track thumbnails by identity rather than by array offset.
/// Offset-based identity is what let a removed-then-re-added photo leave a stale
/// index behind, which is the crash class this type exists to prevent.
struct BatchPhoto: Identifiable, Equatable {
    let id: UUID
    let image: UIImage

    init(id: UUID = UUID(), image: UIImage) {
        self.id = id
        self.image = image
    }

    static func == (lhs: BatchPhoto, rhs: BatchPhoto) -> Bool {
        lhs.id == rhs.id
    }
}

/// The value-type owner of the capture batch. Every mutation the capture screen
/// performs (add from camera, add from Photos, remove a thumbnail) goes through
/// here so the logic is unit-testable and cannot trap on a stale index.
struct PhotoBatch: Equatable {

    private(set) var photos: [BatchPhoto]

    init(_ photos: [BatchPhoto] = []) {
        self.photos = photos
    }

    var isEmpty: Bool { photos.isEmpty }
    var count: Int { photos.count }

    /// The underlying images in batch order, for the scan pipeline.
    var images: [UIImage] { photos.map(\.image) }

    mutating func append(_ image: UIImage) {
        photos.append(BatchPhoto(image: image))
    }

    mutating func append(contentsOf images: [UIImage]) {
        photos.append(contentsOf: images.map { BatchPhoto(image: $0) })
    }

    /// Remove the photo with this identity. A no-op when it is already gone, so a
    /// double tap or a stale SwiftUI closure can never crash.
    mutating func remove(id: UUID) {
        photos.removeAll { $0.id == id }
    }

    /// Remove by array position. Out-of-range indices are ignored rather than
    /// trapping, so a stale index captured by a SwiftUI row closure is harmless.
    mutating func remove(at index: Int) {
        guard photos.indices.contains(index) else { return }
        photos.remove(at: index)
    }

    /// The 1-based position of a photo, for accessibility labels.
    func position(of photo: BatchPhoto) -> Int {
        (photos.firstIndex(of: photo) ?? 0) + 1
    }
}
