import Foundation

/// Diet options offered on the Settings screen.
enum DietPreference: String, CaseIterable, Identifiable {
    case none
    case vegetarian
    case vegan
    case pescatarian
    case keto
    case glutenFree

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .none: return "No preference"
        case .vegetarian: return "Vegetarian"
        case .vegan: return "Vegan"
        case .pescatarian: return "Pescatarian"
        case .keto: return "Keto"
        case .glutenFree: return "Gluten free"
        }
    }
}

/// How the inventory list is ordered.
enum InventorySort: String, CaseIterable, Identifiable {
    case category
    case dateAdded
    case name

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .category: return "Category"
        case .dateAdded: return "Date added"
        case .name: return "Name"
        }
    }

    var symbolName: String {
        switch self {
        case .category: return "square.grid.2x2"
        case .dateAdded: return "clock"
        case .name: return "textformat"
        }
    }
}

/// UserDefaults keys shared between Settings, onboarding and the prompt builder.
enum PrefKey {
    static let diet = "dietPreference"
    static let allergies = "allergies"
    static let cuisineLikes = "cuisineLikes"
    static let householdSize = "householdSize"
    static let hasSeenModelSetup = "hasSeenModelSetup"
    static let hasSeenOnboarding = "hasSeenOnboarding"
    static let inventorySort = "inventorySortOrder"
    /// Hidden debug switch that forces a malformed recipe response so the error
    /// UI can be demonstrated. Off in normal use.
    static let debugSimulateFailure = "debugSimulateRecipeFailure"
    // Model selection uses ModelFileLocator.selectedModelKey.

    /// True once the user has completed (or skipped) the taste profile wizard.
    static let hasCompletedTasteProfile = "hasCompletedTasteProfile"

    /// Default number of people to cook for when the user has not set a size.
    static let defaultHouseholdSize = 2
}

/// A snapshot of the preferences that shape a recipe prompt. Pure value type so
/// the prompt builder is fully testable without touching UserDefaults.
struct RecipePreferences: Equatable {
    var diet: String
    var allergies: String
    var cuisines: String
    var householdSize: Int

    static func current(_ defaults: UserDefaults = .standard) -> RecipePreferences {
        let size = defaults.integer(forKey: PrefKey.householdSize)
        return RecipePreferences(
            diet: defaults.string(forKey: PrefKey.diet) ?? DietPreference.none.rawValue,
            allergies: defaults.string(forKey: PrefKey.allergies) ?? "",
            cuisines: defaults.string(forKey: PrefKey.cuisineLikes) ?? "",
            householdSize: size == 0 ? PrefKey.defaultHouseholdSize : size
        )
    }
}
