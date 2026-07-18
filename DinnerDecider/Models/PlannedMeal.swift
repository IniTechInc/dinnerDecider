import Foundation
import SwiftData

/// A recipe saved to the user's meal plan and synced to their calendar.
/// Persists the full recipe so the deep link can display it without the
/// model needing to regenerate.
@Model
final class PlannedMeal {
    var id: UUID
    var recipeName: String
    /// Ingredient names, one per entry.
    var ingredientNames: [String]
    /// Cooking steps in order.
    var steps: [String]
    var timeMinutes: Int
    var plannedDate: Date
    /// EKEvent.eventIdentifier, used to update or delete the calendar event.
    var calendarEventId: String?

    init(
        from recipe: RecipeSuggestion,
        date: Date
    ) {
        self.id = UUID()
        self.recipeName = recipe.name
        self.ingredientNames = recipe.ingredients.map(\.name)
        self.steps = recipe.steps
        self.timeMinutes = recipe.timeMinutes
        self.plannedDate = date
    }

    /// Format the recipe for calendar event notes.
    var calendarNotes: String {
        var lines: [String] = []
        lines.append("⏱ \(timeMinutes) min")
        lines.append("")
        lines.append("Ingredients:")
        for name in ingredientNames {
            lines.append("• \(name)")
        }
        lines.append("")
        lines.append("Steps:")
        for (i, step) in steps.enumerated() {
            lines.append("\(i + 1). \(step)")
        }
        return lines.joined(separator: "\n")
    }
}
