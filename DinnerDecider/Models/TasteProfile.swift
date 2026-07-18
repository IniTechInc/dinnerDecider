import Foundation

/// The user's taste preferences, collected via the wizard on first launch and
/// editable from Settings. Stored as JSON in UserDefaults.
///
/// Each category is a list of tapped pills plus a free-text "other" field so
/// people can add anything the presets missed.
struct TasteProfile: Codable, Equatable {
    var favoriteFoods: [String] = []
    var favoriteFoodsOther: String = ""
    var dislikedFoods: [String] = []
    var dislikedFoodsOther: String = ""
    var cuisinesLoved: [String] = []
    var cuisinesLovedOther: String = ""
    var cuisinesAvoided: [String] = []
    var cuisinesAvoidedOther: String = ""
    var allergies: [String] = []
    var allergiesOther: String = ""
    var texturesAvoided: [String] = []
    var texturesAvoidedOther: String = ""
    /// 1 (mild) to 5 (bring it on).
    var spiceLevel: Int = 3

    /// True once the user has answered anything at all: any pill selected in any
    /// category, or any "other" free-text filled in. This gates whether the
    /// profile is fed to the model, so it must cover every category (an earlier
    /// version only checked favorites/dislikes, which silently dropped
    /// allergies-only profiles: a safety bug).
    var hasContent: Bool {
        let pillGroups = [favoriteFoods, dislikedFoods, cuisinesLoved, cuisinesAvoided, allergies, texturesAvoided]
        if pillGroups.contains(where: { !$0.isEmpty }) { return true }
        let otherFields = [favoriteFoodsOther, dislikedFoodsOther, cuisinesLovedOther, cuisinesAvoidedOther, allergiesOther, texturesAvoidedOther]
        return otherFields.contains { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
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

    /// Combine a category's pills and its trimmed "other" text into one comma
    /// list. Returns nil when the category is empty so callers can skip the line.
    private func combined(_ pills: [String], _ other: String) -> String? {
        var parts = pills
        let trimmed = other.trimmingCharacters(in: .whitespaces)
        if !trimmed.isEmpty { parts.append(trimmed) }
        return parts.isEmpty ? nil : parts.joined(separator: ", ")
    }

    /// Build the prompt fragment that describes this user's taste to the model.
    func promptFragment() -> String {
        var lines: [String] = []
        if let fav = combined(favoriteFoods, favoriteFoodsOther) {
            lines.append("Favorite foods: \(fav).")
        }
        if let dislikes = combined(dislikedFoods, dislikedFoodsOther) {
            lines.append("Foods the user dislikes: \(dislikes). Avoid these.")
        }
        if let loved = combined(cuisinesLoved, cuisinesLovedOther) {
            lines.append("Cuisines they enjoy: \(loved). Use these as flavor inspiration.")
        }
        if let avoided = combined(cuisinesAvoided, cuisinesAvoidedOther) {
            lines.append("Cuisines they avoid: \(avoided). Avoid those styles.")
        }
        if let allergy = combined(allergies, allergiesOther) {
            lines.append("Allergies: \(allergy). Never include these.")
        }
        if let textures = combined(texturesAvoided, texturesAvoidedOther) {
            lines.append("Textures/flavors they avoid: \(textures).")
        }
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
