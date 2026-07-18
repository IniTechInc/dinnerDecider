import XCTest
@testable import DinnerDecider

final class RecipePromptTests: XCTestCase {

    private func prefs(
        diet: String = DietPreference.none.rawValue,
        allergies: String = "",
        cuisines: String = "",
        household: Int = 2
    ) -> RecipePreferences {
        RecipePreferences(diet: diet, allergies: allergies, cuisines: cuisines, householdSize: household)
    }

    func testPromptListsInventoryAsCommaList() {
        let prompt = AppModel.recipePrompt(itemNames: ["Eggs", "Milk", "Cheese"], prefs: prefs())
        XCTAssertTrue(prompt.contains("Inventory: Eggs, Milk, Cheese."))
    }

    func testPromptIncludesPortionScalingForHousehold() {
        let prompt = AppModel.recipePrompt(itemNames: ["Eggs"], prefs: prefs(household: 4))
        XCTAssertTrue(prompt.contains("serve 4"))
    }

    func testPromptOmitsDietWhenNone() {
        let prompt = AppModel.recipePrompt(itemNames: ["Eggs"], prefs: prefs())
        XCTAssertFalse(prompt.contains("Diet:"))
    }

    func testPromptIncludesDietAllergiesAndCuisines() {
        let prompt = AppModel.recipePrompt(
            itemNames: ["Eggs"],
            prefs: prefs(diet: "vegetarian", allergies: "peanuts", cuisines: "Thai")
        )
        XCTAssertTrue(prompt.contains("Diet: vegetarian."))
        XCTAssertTrue(prompt.contains("Avoid (allergies): peanuts."))
        XCTAssertTrue(prompt.contains("Preferred cuisines: Thai."))
    }

    func testPromptAlwaysDemandsJSON() {
        let prompt = AppModel.recipePrompt(itemNames: ["Eggs"], prefs: prefs())
        XCTAssertTrue(prompt.contains("Respond ONLY with JSON"))
        XCTAssertTrue(prompt.contains("makeNow"))
        XCTAssertTrue(prompt.contains("almostThere"))
    }

    func testPromptNeverContainsEmDash() {
        let prompt = AppModel.recipePrompt(
            itemNames: ["Eggs"],
            prefs: prefs(diet: "keto", allergies: "shellfish", cuisines: "Italian", household: 3)
        )
        XCTAssertFalse(prompt.contains("\u{2014}"))
    }

    /// Field feedback: inventory-only generation produced fake dishes ("cheesy
    /// Italian dip" from ketchup + cheddar). The prompt must demand real dishes
    /// with genuine ingredients and honest hasIt marking against the inventory.
    func testPromptDemandsRealDishesWithHonestInventoryMarking() {
        let prompt = AppModel.recipePrompt(itemNames: ["Eggs"], prefs: prefs())
        XCTAssertTrue(prompt.contains("real, well-known dishes"))
        XCTAssertTrue(prompt.contains("Never invent a dish"))
        XCTAssertTrue(prompt.contains("hasIt true only if it is in the inventory"))
        XCTAssertFalse(prompt.contains("uses only inventory items"))
    }
}
