import XCTest
@testable import DinnerDecider

/// The scan-wide cap on heavy model calls (field fix: an 8GB phone jetsammed
/// on the fifth heavy call of a scan, so a scan gets at most four).
final class ScanCallBudgetTests: XCTestCase {

    func testDefaultBudgetAllowsExactlyFourCalls() {
        var budget = ScanCallBudget()
        XCTAssertTrue(budget.consume())   // whole-image pass
        XCTAssertTrue(budget.consume())   // crop 1
        XCTAssertTrue(budget.consume())   // crop 2
        XCTAssertTrue(budget.consume())   // crop 3
        XCTAssertFalse(budget.consume())  // fifth call: the one that crashed
        XCTAssertEqual(budget.remaining, 0)
    }

    func testRemainingDrivesCropCapping() {
        var budget = ScanCallBudget()
        _ = budget.consume()              // whole-image pass spends one
        XCTAssertEqual(budget.remaining, 3)

        let nineCrops = Array(repeating: "crop", count: 9)
        let capped = Array(nineCrops.prefix(budget.remaining))
        XCTAssertEqual(capped.count, 3)
    }

    func testExhaustedBudgetStaysExhausted() {
        var budget = ScanCallBudget(limit: 1)
        XCTAssertTrue(budget.consume())
        XCTAssertFalse(budget.consume())
        XCTAssertFalse(budget.consume())
        XCTAssertEqual(budget.remaining, 0)
    }
}
