import Foundation
import SwiftData

@Model
final class FoodItem {
    var name: String
    var brand: String?
    var category: FoodCategory
    var quantity: Int
    var dateAdded: Date
    var sourcePhotoData: Data?

    init(
        name: String,
        brand: String? = nil,
        category: FoodCategory = .other,
        quantity: Int = 1,
        dateAdded: Date = .now,
        sourcePhotoData: Data? = nil
    ) {
        self.name = name
        self.brand = brand
        self.category = category
        self.quantity = quantity
        self.dateAdded = dateAdded
        self.sourcePhotoData = sourcePhotoData
    }
}

enum FoodCategory: String, Codable, CaseIterable {
    case produce
    case dairy
    case meat
    case pantry
    case snack
    case beverage
    case condiment
    case frozen
    case other

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

    var systemImage: String {
        switch self {
        case .produce: return "leaf"
        case .dairy: return "cup.and.saucer"
        case .meat: return "fork.knife"
        case .pantry: return "cabinet"
        case .snack: return "bag"
        case .beverage: return "waterbottle"
        case .condiment: return "popcorn"
        case .frozen: return "snowflake"
        case .other: return "shippingbox"
        }
    }
}
