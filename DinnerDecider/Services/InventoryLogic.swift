import Foundation

/// Pure, unit-testable rules for the inventory and shopping list.
///
/// Everything here is free of SwiftUI, SwiftData and UserDefaults so the view
/// layer can stay thin and the tricky bits (dedupe, quantity merges, cooking
/// decrements, shopping aggregation, portion scaling) are covered by fast tests.
enum InventoryLogic {

    /// Normalised key used to decide whether two items are "the same" item.
    /// Case- and whitespace-insensitive on name + brand.
    static func mergeKey(name: String, brand: String?) -> String {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let b = (brand ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return "\(n)|\(b)"
    }

    /// Name-only key, used to match recipe ingredients (which have no brand)
    /// against inventory when cooking.
    static func nameKey(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }

    // MARK: - Scan dedupe

    /// One identified item plus how many crops resolved to it.
    struct Grouped: Equatable {
        let item: IdentifiedItem
        let count: Int
    }

    /// Collapse repeated identifications (the same item seen in overlapping
    /// crops) into unique rows, keeping the first occurrence and the highest
    /// confidence seen for it. Order is preserved.
    static func dedupe(_ items: [IdentifiedItem]) -> [IdentifiedItem] {
        dedupeWithCounts(items).map(\.item)
    }

    /// Like `dedupe` but also reports how many raw identifications backed each
    /// unique row, so quantities can be merged sensibly on confirm.
    static func dedupeWithCounts(_ items: [IdentifiedItem]) -> [Grouped] {
        var order: [String] = []
        var chosen: [String: IdentifiedItem] = [:]
        var counts: [String: Int] = [:]

        for item in items {
            let key = mergeKey(name: item.name, brand: item.brand)
            if let existing = chosen[key] {
                counts[key, default: 1] += 1
                // Keep the identification with the higher confidence.
                if item.confidence > existing.confidence {
                    chosen[key] = item
                }
            } else {
                order.append(key)
                chosen[key] = item
                counts[key] = 1
            }
        }
        return order.map { Grouped(item: chosen[$0]!, count: counts[$0] ?? 1) }
    }

    // MARK: - Merge on confirm

    /// A lightweight view of an existing inventory row, for merge planning.
    struct StockRef: Equatable {
        let key: String
        let quantity: Int
    }

    /// The decision for one item being confirmed into inventory.
    enum ConfirmAction: Equatable {
        /// Bump an existing row's quantity to `newQuantity`.
        case increment(key: String, newQuantity: Int)
        /// Insert a brand new row with `quantity`.
        case insert(key: String, quantity: Int)
    }

    /// Decide, for each confirmed item, whether to bump an existing inventory
    /// row (duplicate names merge quantities) or insert a new one.
    static func confirmPlan(
        confirming: [(key: String, quantity: Int)],
        existing: [StockRef]
    ) -> [ConfirmAction] {
        // Running tally so two confirmed items with the same key stack correctly.
        var quantities: [String: Int] = [:]
        var known = Set<String>()
        for ref in existing {
            quantities[ref.key] = ref.quantity
            known.insert(ref.key)
        }

        var actions: [ConfirmAction] = []
        for entry in confirming {
            let current = quantities[entry.key] ?? 0
            let updated = current + entry.quantity
            quantities[entry.key] = updated
            if known.contains(entry.key) {
                actions.append(.increment(key: entry.key, newQuantity: updated))
            } else {
                known.insert(entry.key)
                actions.append(.insert(key: entry.key, quantity: entry.quantity))
            }
        }
        return actions
    }

    // MARK: - Cooking (quantity decrement)

    /// The result of cooking a recipe for one inventory row.
    struct CookChange: Equatable {
        let key: String
        let newQuantity: Int
        /// True when the row hit zero and should be removed.
        let removed: Bool
    }

    /// Given the keys of the ingredients actually used (the ones the user has)
    /// and the current stock, decrement each matching row by one. Rows that
    /// reach zero are flagged for removal. Rows not used are left untouched and
    /// are not returned.
    static func cookPlan(usedKeys: [String], stock: [StockRef]) -> [CookChange] {
        var quantities: [String: Int] = [:]
        var order: [String] = []
        for ref in stock {
            quantities[ref.key] = ref.quantity
            order.append(ref.key)
        }

        var touched = Set<String>()
        for key in usedKeys where quantities[key] != nil {
            quantities[key]! -= 1
            touched.insert(key)
        }

        return order.compactMap { key in
            guard touched.contains(key) else { return nil }
            let quantity = max(quantities[key] ?? 0, 0)
            return CookChange(key: key, newQuantity: quantity, removed: quantity <= 0)
        }
    }

    // MARK: - Portion scaling

    /// The prompt line describing how many people to cook for. `nil` for an
    /// unset or nonsensical size so the prompt stays compact.
    static func portionLine(householdSize: Int) -> String? {
        guard householdSize >= 1 else { return nil }
        if householdSize == 1 {
            return "Cooking for 1 person; use single-serving portions."
        }
        return "Cooking for \(householdSize) people; scale ingredient amounts to serve \(householdSize)."
    }
}
