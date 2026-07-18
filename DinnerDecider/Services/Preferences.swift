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

/// UserDefaults keys shared between Settings and the prompt builder.
enum PrefKey {
    static let diet = "dietPreference"
    static let allergies = "allergies"
    static let cuisineLikes = "cuisineLikes"
    static let hasSeenModelSetup = "hasSeenModelSetup"
    // Model selection uses ModelFileLocator.selectedModelKey.
}
