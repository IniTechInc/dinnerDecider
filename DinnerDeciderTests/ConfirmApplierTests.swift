import SwiftData
import XCTest
@testable import DinnerDecider

/// Found while porting the confirm flow to Go: `confirmPlan` produces the right
/// actions, but the applier could silently drop one of them.
///
/// `confirmPlan` deliberately emits BOTH an insert and an increment when the same
/// key appears twice in one batch (`testConfirmStacksRepeatedKeysAcrossTheBatch`
/// pins `[insert(eggs|, 1), increment(eggs|, 2)]`). The applier resolved the
/// increment against the SwiftData `@Query` snapshot, which does not refresh mid
/// loop, so the row the insert had just created was invisible and the increment
/// found nothing. Scanning the same item twice landed quantity 1 instead of 2.
///
/// The failure is silent: no error, no crash, just a quantity that is quietly
/// wrong. These tests apply real plans against an in-memory store.
@MainActor
final class ConfirmApplierTests: XCTestCase {

    private func makeContext() throws -> ModelContext {
        let container = try ModelContainer(
            for: InventoryItem.self,
            configurations: ModelConfiguration(isStoredInMemoryOnly: true)
        )
        return ModelContext(container)
    }

    private func fetch(_ context: ModelContext) throws -> [InventoryItem] {
        try context.fetch(FetchDescriptor<InventoryItem>())
    }

    /// The regression. Two identical items in one batch must total 2.
    func testSameItemTwiceInOneBatchAccumulates() throws {
        let context = try makeContext()
        let entries = [
            ConfirmEntry(name: "Eggs", brand: nil, category: .produce),
            ConfirmEntry(name: "Eggs", brand: nil, category: .produce),
        ]

        ConfirmApplier.apply(entries: entries, existing: [], context: context, photoID: "p1")

        let items = try fetch(context)
        XCTAssertEqual(items.count, 1, "The two entries share a merge key, so they are one row")
        XCTAssertEqual(items.first?.quantity, 2, "Both entries must be counted; the increment must not be dropped")
    }

    /// Three of the same item, to prove the running total is not just off by one.
    func testSameItemThreeTimesAccumulates() throws {
        let context = try makeContext()
        let entries = Array(repeating: ConfirmEntry(name: "Eggs", brand: nil, category: .produce), count: 3)

        ConfirmApplier.apply(entries: entries, existing: [], context: context, photoID: "p1")

        XCTAssertEqual(try fetch(context).first?.quantity, 3)
    }

    /// The path that always worked: incrementing a row that already existed.
    func testIncrementsAnExistingRow() throws {
        let context = try makeContext()
        let existing = InventoryItem(name: "Eggs", quantity: 5)
        context.insert(existing)

        ConfirmApplier.apply(
            entries: [ConfirmEntry(name: "Eggs", brand: nil, category: .produce)],
            existing: [existing],
            context: context,
            photoID: "p1"
        )

        let items = try fetch(context)
        XCTAssertEqual(items.count, 1)
        XCTAssertEqual(items.first?.quantity, 6)
    }

    /// Brand is part of the merge key, so these are two distinct rows.
    func testDifferentBrandsStayDistinct() throws {
        let context = try makeContext()
        let entries = [
            ConfirmEntry(name: "Milk", brand: "Horizon", category: .dairy),
            ConfirmEntry(name: "Milk", brand: "Clover", category: .dairy),
        ]

        ConfirmApplier.apply(entries: entries, existing: [], context: context, photoID: "p1")

        let items = try fetch(context)
        XCTAssertEqual(items.count, 2)
        XCTAssertTrue(items.allSatisfy { $0.quantity == 1 })
    }

    /// A blank name is skipped, and must not consume the increment meant for a
    /// real row.
    func testBlankNamesAreSkipped() throws {
        let context = try makeContext()
        let entries = [
            ConfirmEntry(name: "   ", brand: nil, category: .other),
            ConfirmEntry(name: "Eggs", brand: nil, category: .produce),
        ]

        ConfirmApplier.apply(entries: entries, existing: [], context: context, photoID: "p1")

        let items = try fetch(context)
        XCTAssertEqual(items.count, 1)
        XCTAssertEqual(items.first?.name, "Eggs")
        XCTAssertEqual(items.first?.quantity, 1)
    }

    /// Whitespace around a name must not split one row into two: `mergeKey`
    /// trims, so the stored name is trimmed but the key still matches.
    func testWhitespaceDoesNotSplitARow() throws {
        let context = try makeContext()
        let entries = [
            ConfirmEntry(name: "Eggs", brand: nil, category: .produce),
            ConfirmEntry(name: "  Eggs  ", brand: nil, category: .produce),
        ]

        ConfirmApplier.apply(entries: entries, existing: [], context: context, photoID: "p1")

        let items = try fetch(context)
        XCTAssertEqual(items.count, 1)
        XCTAssertEqual(items.first?.quantity, 2)
    }
}
