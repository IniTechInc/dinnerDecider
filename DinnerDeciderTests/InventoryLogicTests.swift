import XCTest
@testable import DinnerDecider

final class InventoryLogicTests: XCTestCase {

    private func item(_ name: String, brand: String? = nil, confidence: Double = 0.9) -> IdentifiedItem {
        IdentifiedItem(name: name, brand: brand, category: "other", confidence: confidence)
    }

    // MARK: - Keys

    func testMergeKeyIsCaseAndWhitespaceInsensitive() {
        XCTAssertEqual(
            InventoryLogic.mergeKey(name: "  Whole Milk ", brand: " Horizon "),
            InventoryLogic.mergeKey(name: "whole milk", brand: "horizon")
        )
    }

    func testMergeKeyDistinguishesBrand() {
        XCTAssertNotEqual(
            InventoryLogic.mergeKey(name: "Milk", brand: "Horizon"),
            InventoryLogic.mergeKey(name: "Milk", brand: "Fairlife")
        )
    }

    func testNameKeyIgnoresBrand() {
        XCTAssertEqual(InventoryLogic.nameKey("  Olive Oil "), "olive oil")
    }

    // MARK: - Dedupe

    func testDedupeCollapsesDuplicatesPreservingOrder() {
        let input = [item("Milk", brand: "Horizon"), item("Eggs"), item("milk", brand: "horizon")]
        let result = InventoryLogic.dedupe(input)
        XCTAssertEqual(result.map(\.name), ["Milk", "Eggs"])
    }

    func testDedupeWithCountsTalliesDuplicates() {
        let input = [item("Milk"), item("Milk"), item("Eggs")]
        let grouped = InventoryLogic.dedupeWithCounts(input)
        XCTAssertEqual(grouped.count, 2)
        XCTAssertEqual(grouped[0].count, 2)
        XCTAssertEqual(grouped[1].count, 1)
    }

    func testDedupeKeepsHighestConfidence() {
        let input = [item("Milk", confidence: 0.4), item("Milk", confidence: 0.95)]
        let result = InventoryLogic.dedupe(input)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].confidence, 0.95, accuracy: 0.0001)
    }

    // MARK: - Confirm / merge quantities

    func testConfirmInsertsWhenNoExistingMatch() {
        let plan = InventoryLogic.confirmPlan(
            confirming: [(key: "milk|", quantity: 1)],
            existing: []
        )
        XCTAssertEqual(plan, [.insert(key: "milk|", quantity: 1)])
    }

    func testConfirmIncrementsExistingQuantity() {
        let plan = InventoryLogic.confirmPlan(
            confirming: [(key: "milk|horizon", quantity: 1)],
            existing: [InventoryLogic.StockRef(key: "milk|horizon", quantity: 2)]
        )
        XCTAssertEqual(plan, [.increment(key: "milk|horizon", newQuantity: 3)])
    }

    func testConfirmStacksRepeatedKeysAcrossTheBatch() {
        // Two confirmed items with the same key: first inserts, second bumps.
        let plan = InventoryLogic.confirmPlan(
            confirming: [(key: "eggs|", quantity: 1), (key: "eggs|", quantity: 1)],
            existing: []
        )
        XCTAssertEqual(plan, [.insert(key: "eggs|", quantity: 1), .increment(key: "eggs|", newQuantity: 2)])
    }

    // MARK: - Cooking decrement

    func testCookPlanDecrementsUsedItems() {
        let changes = InventoryLogic.cookPlan(
            usedKeys: ["eggs", "cheese"],
            stock: [
                InventoryLogic.StockRef(key: "eggs", quantity: 6),
                InventoryLogic.StockRef(key: "cheese", quantity: 2),
                InventoryLogic.StockRef(key: "milk", quantity: 1)
            ]
        )
        XCTAssertEqual(changes, [
            InventoryLogic.CookChange(key: "eggs", newQuantity: 5, removed: false),
            InventoryLogic.CookChange(key: "cheese", newQuantity: 1, removed: false)
        ])
    }

    func testCookPlanFlagsItemsThatReachZeroForRemoval() {
        let changes = InventoryLogic.cookPlan(
            usedKeys: ["milk"],
            stock: [InventoryLogic.StockRef(key: "milk", quantity: 1)]
        )
        XCTAssertEqual(changes, [InventoryLogic.CookChange(key: "milk", newQuantity: 0, removed: true)])
    }

    func testCookPlanIgnoresIngredientsNotInStock() {
        let changes = InventoryLogic.cookPlan(
            usedKeys: ["saffron"],
            stock: [InventoryLogic.StockRef(key: "milk", quantity: 1)]
        )
        XCTAssertTrue(changes.isEmpty)
    }

    // MARK: - Portion scaling

    func testPortionLineSingleServing() {
        XCTAssertEqual(
            InventoryLogic.portionLine(householdSize: 1),
            "Cooking for 1 person; use single-serving portions."
        )
    }

    func testPortionLineMultiplePeople() {
        XCTAssertEqual(
            InventoryLogic.portionLine(householdSize: 4),
            "Cooking for 4 people; scale ingredient amounts to serve 4."
        )
    }

    func testPortionLineNilForZeroOrNegative() {
        XCTAssertNil(InventoryLogic.portionLine(householdSize: 0))
        XCTAssertNil(InventoryLogic.portionLine(householdSize: -3))
    }
}
