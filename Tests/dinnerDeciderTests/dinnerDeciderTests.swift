import Testing
@testable import dinnerDecider

struct FoodItemTests {
    @Test func foodCategoryDisplayNames() {
        for category in FoodCategory.allCases {
            #expect(!category.displayName.isEmpty)
            #expect(!category.systemImage.isEmpty)
        }
    }

    @Test func scannedItemUnknownFallback() {
        let item = ScannedItem.unknown()
        #expect(item.name == "Unknown item")
        #expect(item.confidence == 0.1)
        #expect(item.category == .other)
    }
}
