import XCTest
@testable import DinnerDecider

final class TasteProfileTests: XCTestCase {

    // MARK: - hasContent

    /// The regression that mattered: an allergies-only profile used to report
    /// "not complete" and was silently dropped from the prompt, which is a
    /// safety issue. It must now count as having content.
    func testHasContentTrueForAllergiesOnly() {
        var profile = TasteProfile()
        profile.allergies = ["Peanuts"]
        XCTAssertTrue(profile.hasContent)
    }

    func testHasContentTrueForOtherTextOnly() {
        var profile = TasteProfile()
        profile.favoriteFoodsOther = "  ramen  "
        XCTAssertTrue(profile.hasContent)
    }

    func testHasContentFalseWhenEmpty() {
        // Default spice level alone is not "content".
        XCTAssertFalse(TasteProfile().hasContent)
    }

    func testHasContentFalseForWhitespaceOtherText() {
        var profile = TasteProfile()
        profile.dislikedFoodsOther = "   "
        XCTAssertFalse(profile.hasContent)
    }

    // MARK: - promptFragment

    func testPromptFragmentJoinsPillsAndOtherText() {
        var profile = TasteProfile()
        profile.favoriteFoods = ["Pizza", "Tacos"]
        profile.favoriteFoodsOther = "ramen"
        let fragment = profile.promptFragment()
        XCTAssertTrue(fragment.contains("Favorite foods: Pizza, Tacos, ramen."))
    }

    func testPromptFragmentAllergyLinePresentWhenOnlyAllergiesSet() {
        var profile = TasteProfile()
        profile.allergies = ["Peanuts", "Shellfish"]
        let fragment = profile.promptFragment()
        XCTAssertTrue(fragment.contains("Allergies: Peanuts, Shellfish. Never include these."))
    }

    func testPromptFragmentOmitsEmptyCategories() {
        var profile = TasteProfile()
        profile.allergies = ["Peanuts"]
        let fragment = profile.promptFragment()
        XCTAssertFalse(fragment.contains("Favorite foods:"))
        XCTAssertFalse(fragment.contains("Foods the user dislikes:"))
        XCTAssertFalse(fragment.contains("Cuisines they enjoy:"))
    }

    func testPromptFragmentAlwaysIncludesSpiceLine() {
        // Even a completely empty profile still states a spice preference.
        XCTAssertTrue(TasteProfile().promptFragment().contains("Spice preference:"))
    }

    func testPromptFragmentNeverContainsEmDash() {
        var profile = TasteProfile()
        profile.favoriteFoods = ["Pizza"]
        profile.dislikedFoods = ["Liver"]
        profile.cuisinesLoved = ["Italian"]
        profile.cuisinesAvoided = ["Fast food"]
        profile.allergies = ["Peanuts"]
        profile.texturesAvoided = ["Mushy"]
        profile.allergiesOther = "kiwi"
        XCTAssertFalse(profile.promptFragment().contains("\u{2014}"))
    }
}
