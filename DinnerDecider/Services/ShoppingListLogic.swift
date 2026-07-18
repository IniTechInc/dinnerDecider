import Foundation

/// Pure helpers for building and exporting the shopping list.
enum ShoppingListLogic {

    /// De-duplicate a set of ingredient names case-insensitively, dropping blanks
    /// and preserving first-seen order. Used when adding an "almost there"
    /// recipe's missing items to the list.
    static func aggregate(_ names: [String]) -> [String] {
        var seen = Set<String>()
        var result: [String] = []
        for raw in names {
            let name = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !name.isEmpty else { continue }
            let key = name.lowercased()
            if !seen.contains(key) {
                seen.insert(key)
                result.append(name)
            }
        }
        return result
    }

    /// Given the names already on the list, return only the new ones to add.
    static func newItems(adding names: [String], existing: [String]) -> [String] {
        let existingKeys = Set(existing.map { $0.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() })
        return aggregate(names).filter { !existingKeys.contains($0.lowercased()) }
    }

    /// A plain line for export, decoupled from SwiftData for testing.
    struct Line: Equatable {
        let name: String
        let isChecked: Bool
    }

    /// Render the list as plain text for the share sheet. Checked items are
    /// marked done so a shared list still reads clearly.
    static func exportText(_ lines: [Line]) -> String {
        guard !lines.isEmpty else { return "Shopping List\n\n(empty)" }
        var out = "Shopping List\n\n"
        for line in lines {
            let box = line.isChecked ? "[x]" : "[ ]"
            out += "\(box) \(line.name)\n"
        }
        return out.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
