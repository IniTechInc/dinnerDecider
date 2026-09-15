import XCTest
@testable import DinnerDecider

/// TestFlight feedback P2: a scan whose model calls all FAILED was reported
/// with the same "Nothing spotted" empty state as a scan of a genuinely empty
/// fridge. Users were told to add light and move closer when the real problem
/// was that the model never answered. The two cases must be distinguishable.
final class ScanOutcomeTests: XCTestCase {

    func testItemsFoundFinishes() {
        XCTAssertEqual(
            ScanOutcome.terminalPhase(itemsFound: 3, inferenceFailures: 0),
            .finished
        )
    }

    /// Partial failure still counts as a success: the user gets what was found
    /// rather than an error that throws away real results.
    func testItemsFoundWinsOverFailures() {
        XCTAssertEqual(
            ScanOutcome.terminalPhase(itemsFound: 2, inferenceFailures: 5),
            .finished
        )
    }

    /// The genuinely-empty case: every call answered, none of them saw food.
    /// This is the only case where "Nothing spotted" is honest.
    func testNothingFoundAndNoFailuresIsEmpty() {
        XCTAssertEqual(
            ScanOutcome.terminalPhase(itemsFound: 0, inferenceFailures: 0),
            .empty
        )
    }

    /// The regression this type exists to prevent. Before the fix this returned
    /// `.empty`, which told the user their fridge looked empty when in fact
    /// every single inference had thrown.
    func testNothingFoundWithFailuresIsFailedNotEmpty() {
        let phase = ScanOutcome.terminalPhase(itemsFound: 0, inferenceFailures: 4)
        XCTAssertEqual(phase, .failed)
        XCTAssertNotEqual(phase, .empty, "A scan where every model call failed must not claim the fridge was empty")
    }

    /// One swallowed failure is enough to make "Nothing spotted" a lie.
    func testSingleFailureIsEnoughToReportFailure() {
        XCTAssertEqual(
            ScanOutcome.terminalPhase(itemsFound: 0, inferenceFailures: 1),
            .failed
        )
    }

    /// Mirrors the real budget: four calls, all throwing (the build-4 field
    /// report where the model unloaded mid-scan and every identify threw
    /// `modelNotLoaded`).
    func testExhaustedBudgetAllThrowingReportsFailure() {
        var budget = ScanCallBudget()
        var failures = 0
        while budget.consume() { failures += 1 }

        XCTAssertEqual(failures, 4)
        XCTAssertEqual(
            ScanOutcome.terminalPhase(itemsFound: 0, inferenceFailures: failures),
            .failed
        )
    }
}
