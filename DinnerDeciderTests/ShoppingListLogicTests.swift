import XCTest
@testable import DinnerDecider

final class ShoppingListLogicTests: XCTestCase {

    func testAggregateDedupesCaseInsensitivelyPreservingOrder() {
        let result = ShoppingListLogic.aggregate(["Onion", "onion", "Chili Powder", "  ", "ONION"])
        XCTAssertEqual(result, ["Onion", "Chili Powder"])
    }

    func testAggregateTrimsWhitespaceAndDropsBlanks() {
        let result = ShoppingListLogic.aggregate(["  Milk  ", "", "   "])
        XCTAssertEqual(result, ["Milk"])
    }

    func testNewItemsExcludesThingsAlreadyOnList() {
        let result = ShoppingListLogic.newItems(
            adding: ["Onion", "Garlic", "onion"],
            existing: ["ONION", "Salt"]
        )
        XCTAssertEqual(result, ["Garlic"])
    }

    func testExportTextMarksCheckedItems() {
        let lines = [
            ShoppingListLogic.Line(name: "Onion", isChecked: false),
            ShoppingListLogic.Line(name: "Milk", isChecked: true)
        ]
        let text = ShoppingListLogic.exportText(lines)
        XCTAssertTrue(text.contains("Shopping List"))
        XCTAssertTrue(text.contains("[ ] Onion"))
        XCTAssertTrue(text.contains("[x] Milk"))
    }

    func testExportTextHandlesEmptyList() {
        XCTAssertEqual(ShoppingListLogic.exportText([]), "Shopping List\n\n(empty)")
    }
}
