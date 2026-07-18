import Foundation

/// The user's taste preferences, collected via the wizard on first launch and
/// editable from Settings. Stored as JSON in UserDefaults.
struct TasteProfile: Codable, Equatable {
    var favoriteFoods: String = ""
    var leastFavoriteFoods: String = ""
    var favoriteRestaurants: String = ""
    var avoidRestaurants: String = ""
    var allergies: String = ""
    var textureFlavorAvoidances: String = ""
    /// 1 (mild) to 5 (bring it on).
    var spiceLevel: Int = 3

    /// True once the user has filled in at least one field.
    var isComplete: Bool {
        !favoriteFoods.trimmingCharacters(in: .whitespaces).isEmpty
            || !leastFavoriteFoods.trimmingCharacters(in: .whitespaces).isEmpty
    }

    // MARK: - Persistence

    private static let storageKey = "tasteProfile"

    static func load() -> TasteProfile? {
        guard let data = UserDefaults.standard.data(forKey: storageKey) else { return nil }
        return try? JSONDecoder().decode(TasteProfile.self, from: data)
    }

    func save() {
        if let data = try? JSONEncoder().encode(self) {
            UserDefaults.standard.set(data, forKey: Self.storageKey)
        }
    }

    /// Build the prompt fragment that describes this user's taste to the model.
    func promptFragment() -> String {
        var lines: [String] = []
        let fav = favoriteFoods.trimmingCharacters(in: .whitespaces)
        if !fav.isEmpty { lines.append("Favorite foods: \(fav).") }
        let least = leastFavoriteFoods.trimmingCharacters(in: .whitespaces)
        if !least.isEmpty { lines.append("Foods the user dislikes: \(least). Avoid these.") }
        let favRest = favoriteRestaurants.trimmingCharacters(in: .whitespaces)
        if !favRest.isEmpty { lines.append("Restaurants they enjoy: \(favRest). Use these as flavor inspiration.") }
        let avoidRest = avoidRestaurants.trimmingCharacters(in: .whitespaces)
        if !avoidRest.isEmpty { lines.append("Restaurants they avoid: \(avoidRest). Avoid those styles.") }
        let allergy = allergies.trimmingCharacters(in: .whitespaces)
        if !allergy.isEmpty { lines.append("Allergies: \(allergy). Never include these.") }
        let textures = textureFlavorAvoidances.trimmingCharacters(in: .whitespaces)
        if !textures.isEmpty { lines.append("Textures/flavors they avoid: \(textures).") }
        let spiceDesc: String
        switch spiceLevel {
        case 1: spiceDesc = "very mild, no spice at all"
        case 2: spiceDesc = "mild, just a hint of spice"
        case 3: spiceDesc = "medium spice"
        case 4: spiceDesc = "spicy"
        case 5: spiceDesc = "very spicy, the hotter the better"
        default: spiceDesc = "medium spice"
        }
        lines.append("Spice preference: \(spiceDesc).")
        return lines.joined(separator: "\n")
    }
}
