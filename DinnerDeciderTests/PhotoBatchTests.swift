import XCTest
import UIKit
@testable import DinnerDecider

/// Regression tests for the capture-batch state, extracted from `CaptureView` so
/// the "remove a photo, then add another" crash can be reproduced and fixed in
/// isolation (no UI, no camera, no model).
final class PhotoBatchTests: XCTestCase {

    /// A tiny distinct 1x1 image so each photo is a real `UIImage`.
    private func makeImage(_ shade: CGFloat = 0.5) -> UIImage {
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        let renderer = UIGraphicsImageRenderer(size: CGSize(width: 1, height: 1), format: format)
        return renderer.image { ctx in
            UIColor(white: shade, alpha: 1).setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 1, height: 1))
        }
    }

    // MARK: - Basics

    func testAppendGrowsBatch() {
        var batch = PhotoBatch()
        XCTAssertTrue(batch.isEmpty)
        batch.append(makeImage())
        batch.append(makeImage())
        XCTAssertEqual(batch.count, 2)
        XCTAssertEqual(batch.images.count, 2)
    }

    func testAppendContentsOfPreservesOrder() {
        var batch = PhotoBatch()
        let a = makeImage(0.1)
        let b = makeImage(0.9)
        batch.append(contentsOf: [a, b])
        XCTAssertEqual(batch.images.first, a)
        XCTAssertEqual(batch.images.last, b)
    }

    func testEachAppendedPhotoGetsADistinctIdentity() {
        var batch = PhotoBatch()
        batch.append(makeImage())
        batch.append(makeImage())
        XCTAssertNotEqual(batch.photos[0].id, batch.photos[1].id)
    }

    // MARK: - Removal by identity

    func testRemoveByIdentityRemovesExactlyThatPhoto() {
        var batch = PhotoBatch()
        batch.append(makeImage()); batch.append(makeImage()); batch.append(makeImage())
        let middleID = batch.photos[1].id
        batch.remove(id: middleID)
        XCTAssertEqual(batch.count, 2)
        XCTAssertFalse(batch.photos.contains { $0.id == middleID })
    }

    func testRemoveByIdentityIsNoOpForUnknownID() {
        var batch = PhotoBatch()
        batch.append(makeImage())
        batch.remove(id: UUID())        // never in the batch
        XCTAssertEqual(batch.count, 1)
    }

    /// The user's exact path: remove one, then the identity of a still-present
    /// photo must survive so its own remove button still targets the right one.
    func testRemoveByIdentityAfterAnotherRemovalTargetsCorrectPhoto() {
        var batch = PhotoBatch()
        batch.append(makeImage()); batch.append(makeImage()); batch.append(makeImage())
        let firstID = batch.photos[0].id
        let lastID = batch.photos[2].id
        batch.remove(id: firstID)       // array shifts: [1, 2]
        batch.remove(id: lastID)        // identity still finds the original last
        XCTAssertEqual(batch.count, 1)
        XCTAssertFalse(batch.photos.contains { $0.id == lastID })
    }

    // MARK: - Removal by index (the crash)

    /// Reproduces the reported crash. A SwiftUI overlay button captures the row's
    /// index at render time. After one photo is removed the array shrinks, but a
    /// still-live closure (mid-animation, or a quick second tap) can fire with an
    /// index that is now out of range. The old code called `Array.remove(at:)`
    /// directly, which TRAPS. This must be a safe no-op instead.
    func testRemoveWithStaleOutOfRangeIndexDoesNotCrash() {
        var batch = PhotoBatch()
        batch.append(makeImage())
        batch.append(makeImage())       // valid indices 0, 1
        batch.remove(at: 1)             // now only index 0 is valid
        batch.remove(at: 1)             // stale index -> would trap in old code
        XCTAssertEqual(batch.count, 1)
    }

    func testRemoveAtValidIndexRemovesThatPhoto() {
        var batch = PhotoBatch()
        batch.append(makeImage(0.2)); batch.append(makeImage(0.8))
        let survivorID = batch.photos[1].id
        batch.remove(at: 0)
        XCTAssertEqual(batch.count, 1)
        XCTAssertEqual(batch.photos[0].id, survivorID)
    }

    func testRemoveLastPhotoLeavesEmptyBatch() {
        var batch = PhotoBatch()
        batch.append(makeImage())
        batch.remove(id: batch.photos[0].id)
        XCTAssertTrue(batch.isEmpty)
    }

    // MARK: - Remove then re-add

    func testRemoveThenReAddKeepsDistinctIdentities() {
        var batch = PhotoBatch()
        batch.append(makeImage())
        let firstID = batch.photos[0].id
        batch.remove(id: firstID)
        batch.append(makeImage())       // "add another photo" after unselecting
        XCTAssertEqual(batch.count, 1)
        XCTAssertNotEqual(batch.photos[0].id, firstID)
    }

    func testPositionIsOneBased() {
        var batch = PhotoBatch()
        batch.append(makeImage()); batch.append(makeImage())
        XCTAssertEqual(batch.position(of: batch.photos[0]), 1)
        XCTAssertEqual(batch.position(of: batch.photos[1]), 2)
    }
}
