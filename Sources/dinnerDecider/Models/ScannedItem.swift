import Foundation

/// Transient model representing a single Gemma-identified item before it is persisted.
struct ScannedItem: Identifiable, Codable {
    let id: UUID
    var name: String
    var brand: String?
    var category: FoodCategory
    var confidence: Double

    init(id: UUID = .init(), name: String, brand: String? = nil, category: FoodCategory = .other, confidence: Double) {
        self.id = id
        self.name = name
        self.brand = brand
        self.category = category
        self.confidence = confidence
    }
}

extension ScannedItem {
    /// Fallback item emitted when Gemma returns unparseable output.
    static func unknown() -> ScannedItem {
        ScannedItem(name: "Unknown item", brand: nil, category: .other, confidence: 0.1)
    }
}
