import Foundation

/// A single ingredient row in a suggested recipe.
/// `hasIt` marks whether the item is already in the user's inventory.
struct IngredientLine: Codable, Hashable, Identifiable {
    var id: String { name }
    let name: String
    let hasIt: Bool
}

/// A recipe suggestion. Not persisted; produced on the fly by the model.
struct RecipeSuggestion: Codable, Hashable, Identifiable {
    var id: String { name }
    let name: String
    let ingredients: [IngredientLine]
    let steps: [String]
    let timeMinutes: Int
    /// Items the user would need to buy to make this recipe.
    let missingItems: [String]
}

/// The two recipe buckets returned by a single model call.
/// "Make now" needs nothing extra; "Almost there" needs a couple of items.
struct RecipeBundleResponse: Codable {
    let makeNow: [RecipeSuggestion]
    let almostThere: [RecipeSuggestion]
}
