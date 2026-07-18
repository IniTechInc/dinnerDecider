import XCTest
@testable import DinnerDecider

/// Unit tests for the pure, side-effect-free download logic: the disk-space
/// gate, the size-verification decision, the cellular-confirmation gate, and the
/// state machine. URLSession itself is not exercised here.
final class ModelDownloadServiceTests: XCTestCase {

    // MARK: Disk-space gate

    func testDiskSpaceEnoughWithHeadroom() {
        XCTAssertTrue(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes: 5_000_000_000))
    }

    func testDiskSpaceExactBoundaryPasses() {
        XCTAssertTrue(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes: ModelDownloadLogic.requiredFreeBytes))
    }

    func testDiskSpaceJustBelowBoundaryFails() {
        XCTAssertFalse(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes: ModelDownloadLogic.requiredFreeBytes - 1))
    }

    func testDiskSpaceInsufficient() {
        XCTAssertFalse(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes: 1_000_000_000))
    }

    // MARK: Size-verification decision

    func testSizeValidWhenExactMatch() {
        XCTAssertTrue(ModelDownloadLogic.sizeIsValid(actual: 3_460_000_000, expected: 3_460_000_000))
    }

    func testSizeInvalidWhenShort() {
        XCTAssertFalse(ModelDownloadLogic.sizeIsValid(actual: 3_000_000_000, expected: 3_460_000_000))
    }

    func testSizeInvalidWhenLarger() {
        XCTAssertFalse(ModelDownloadLogic.sizeIsValid(actual: 3_460_000_001, expected: 3_460_000_000))
    }

    func testSizeInvalidWhenExpectedUnknown() {
        XCTAssertFalse(ModelDownloadLogic.sizeIsValid(actual: 100, expected: 0))
    }

    // MARK: Cellular-confirmation gate

    func testMayStartOnWifi() {
        XCTAssertTrue(ModelDownloadLogic.mayStartDownload(isCellular: false, userConfirmedCellular: false))
    }

    func testBlockedOnCellularWithoutConfirmation() {
        XCTAssertFalse(ModelDownloadLogic.mayStartDownload(isCellular: true, userConfirmedCellular: false))
    }

    func testAllowedOnCellularWithConfirmation() {
        XCTAssertTrue(ModelDownloadLogic.mayStartDownload(isCellular: true, userConfirmedCellular: true))
    }

    // MARK: Overall fraction

    func testOverallFractionHalf() {
        XCTAssertEqual(ModelDownloadLogic.overallFraction(received: 50, total: 100), 0.5, accuracy: 0.0001)
    }

    func testOverallFractionZeroTotalIsZero() {
        XCTAssertEqual(ModelDownloadLogic.overallFraction(received: 50, total: 0), 0, accuracy: 0.0001)
    }

    func testOverallFractionCapsAtOne() {
        XCTAssertEqual(ModelDownloadLogic.overallFraction(received: 150, total: 100), 1.0, accuracy: 0.0001)
    }

    // MARK: State machine

    func testHappyPathTransitions() {
        var state = ModelDownloadState.notStarted
        state = ModelDownloadLogic.next(state, .start)
        XCTAssertEqual(state, .downloading)
        state = ModelDownloadLogic.next(state, .allDownloaded)
        XCTAssertEqual(state, .verifying)
        state = ModelDownloadLogic.next(state, .verifiedOK)
        XCTAssertEqual(state, .done)
    }

    func testPauseResume() {
        XCTAssertEqual(ModelDownloadLogic.next(.downloading, .pause), .paused)
        XCTAssertEqual(ModelDownloadLogic.next(.paused, .resume), .downloading)
        XCTAssertEqual(ModelDownloadLogic.next(.paused, .start), .downloading)
    }

    func testVerificationFailureGoesToFailed() {
        XCTAssertEqual(
            ModelDownloadLogic.next(.verifying, .verificationFailed("bad size")),
            .failed(reason: "bad size")
        )
    }

    func testFailedFromAnyStateOnError() {
        XCTAssertEqual(ModelDownloadLogic.next(.downloading, .failed("no network")), .failed(reason: "no network"))
        XCTAssertEqual(ModelDownloadLogic.next(.verifying, .failed("io")), .failed(reason: "io"))
        XCTAssertEqual(ModelDownloadLogic.next(.notStarted, .failed("x")), .failed(reason: "x"))
    }

    func testRetryFromFailedRestarts() {
        XCTAssertEqual(ModelDownloadLogic.next(.failed(reason: "x"), .retry), .downloading)
        XCTAssertEqual(ModelDownloadLogic.next(.failed(reason: "x"), .start), .downloading)
    }

    func testUnhandledEventsAreNoOps() {
        XCTAssertEqual(ModelDownloadLogic.next(.notStarted, .pause), .notStarted)
        XCTAssertEqual(ModelDownloadLogic.next(.done, .pause), .done)
        XCTAssertEqual(ModelDownloadLogic.next(.downloading, .resume), .downloading)
        XCTAssertEqual(ModelDownloadLogic.next(.verifying, .pause), .verifying)
    }

    // MARK: File definitions match the locator and the required URLs

    func testFileNamesMatchLocator() {
        XCTAssertEqual(ModelDownloadFile.mmproj.fileName, ModelFileLocator.preferredMmprojName)
        XCTAssertEqual(ModelDownloadFile.weights.fileName, ModelFileLocator.defaultModelPreference[0])
    }

    func testProjectorDownloadsFirst() {
        XCTAssertEqual(ModelDownloadFile.allCases.first, .mmproj)
    }

    func testRemoteURLsAreTheExpectedPublicURLs() {
        XCTAssertEqual(
            ModelDownloadFile.weights.remoteURL.absoluteString,
            "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-UD-IQ3_XXS.gguf"
        )
        XCTAssertEqual(
            ModelDownloadFile.mmproj.remoteURL.absoluteString,
            "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf"
        )
    }

    // MARK: FileDownloadProgress fraction

    func testFileProgressFraction() {
        let progress = FileDownloadProgress(bytesReceived: 25, totalBytes: 100)
        XCTAssertEqual(progress.fraction, 0.25, accuracy: 0.0001)
    }

    func testFileProgressFractionZeroTotal() {
        let progress = FileDownloadProgress(bytesReceived: 25, totalBytes: 0)
        XCTAssertEqual(progress.fraction, 0, accuracy: 0.0001)
    }
}
