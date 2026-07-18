import Foundation
import SwiftData

/// The food category buckets used across the app.
/// Stored on `InventoryItem` as a raw string so SwiftData keeps a stable value
/// even if we reorder or rename cases later.
enum FoodCategory: String, Codable, CaseIterable, Identifiable, Sendable {
    case produce
    case dairy
    case meat
    case pantry
    case snack
    case beverage
    case condiment
    case frozen
    case other

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .produce: return "Produce"
        case .dairy: return "Dairy"
        case .meat: return "Meat"
        case .pantry: return "Pantry"
        case .snack: return "Snacks"
        case .beverage: return "Beverages"
        case .condiment: return "Condiments"
        case .frozen: return "Frozen"
        case .other: return "Other"
        }
    }

    /// SF Symbol used to give each category a friendly icon.
    var symbolName: String {
        switch self {
        case .produce: return "carrot"
        case .dairy: return "drop"
        case .meat: return "fork.knife"
        case .pantry: return "shippingbox"
        case .snack: return "takeoutbag.and.cup.and.straw"
        case .beverage: return "cup.and.saucer"
        case .condiment: return "waterbottle"
        case .frozen: return "snowflake"
        case .other: return "questionmark.circle"
        }
    }
}

@Model
final class InventoryItem {
    var name: String
    var brand: String?
    /// Backing storage for `category`. Kept as a raw string per the spec.
    var categoryRaw: String
    var quantity: Int
    var dateAdded: Date
    var sourcePhotoID: String?
    /// A staple you almost always have (salt, oil). Shown collapsed so it does
    /// not clutter the main list. Defaults to false, which keeps SwiftData
    /// lightweight migration happy for existing stores.
    var isStaple: Bool = false

    /// Typed accessor over the raw category string.
    var category: FoodCategory {
        get { FoodCategory(rawValue: categoryRaw) ?? .other }
        set { categoryRaw = newValue.rawValue }
    }

    /// The merge key used to spot duplicates (name + brand, normalised).
    var mergeKey: String {
        InventoryLogic.mergeKey(name: name, brand: brand)
    }

    init(
        name: String,
        brand: String? = nil,
        category: FoodCategory = .other,
        quantity: Int = 1,
        dateAdded: Date = .now,
        sourcePhotoID: String? = nil,
        isStaple: Bool = false
    ) {
        self.name = name
        self.brand = brand
        self.categoryRaw = category.rawValue
        self.quantity = quantity
        self.dateAdded = dateAdded
        self.sourcePhotoID = sourcePhotoID
        self.isStaple = isStaple
    }
}
