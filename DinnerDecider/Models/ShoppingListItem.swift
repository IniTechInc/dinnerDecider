import Foundation
import SwiftData

/// A single line on the shopping list.
///
/// Persisted with SwiftData so the list survives across launches. Items land
/// here either from an "almost there" recipe (the missing ingredients) or by
/// manual entry. `isChecked` drives the check-off and "clear checked" features.
@Model
final class ShoppingListItem {
    var name: String
    var isChecked: Bool
    var dateAdded: Date
    /// True when the user typed this in by hand rather than adding it from a recipe.
    var isManual: Bool

    init(
        name: String,
        isChecked: Bool = false,
        dateAdded: Date = .now,
        isManual: Bool = false
    ) {
        self.name = name
        self.isChecked = isChecked
        self.dateAdded = dateAdded
        self.isManual = isManual
    }
}
