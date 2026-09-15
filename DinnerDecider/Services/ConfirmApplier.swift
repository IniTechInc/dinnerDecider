import Foundation
import SwiftData

/// One reviewed item on its way into the inventory.
struct ConfirmEntry: Equatable {
    var name: String
    var brand: String?
    var category: FoodCategory
}

/// Applies an `InventoryLogic.confirmPlan` to a SwiftData context.
///
/// This exists as its own type so the apply step is testable. It used to live
/// inline in `ScanningView.addAllToInventory`, where a real bug hid:
///
/// `confirmPlan` deliberately emits BOTH an insert and an increment when the
/// same merge key appears twice in one batch, because dropping either one loses
/// data (`testConfirmStacksRepeatedKeysAcrossTheBatch` pins
/// `[insert(eggs|, 1), increment(eggs|, 2)]`). The old applier resolved every
/// increment against the `@Query` snapshot of existing inventory. That snapshot
/// does not refresh partway through the loop, so the row the insert had just
/// created was invisible, the increment matched nothing, and it was silently
/// dropped. Scanning the same item twice stored quantity 1 instead of 2, with no
/// error and no crash.
///
/// The fix is to make rows inserted during this batch resolvable, which is what
/// `insertedThisBatch` below does. Found while porting the confirm flow to Go,
/// where the same plan was applied against a real database and produced 2.
enum ConfirmApplier {

    /// Apply `entries` to `context`, bumping or inserting rows per the plan.
    ///
    /// - Parameters:
    ///   - entries: the reviewed items, in display order.
    ///   - existing: the inventory rows that existed before this batch.
    ///   - context: the SwiftData context to mutate.
    ///   - photoID: source photo id recorded on every newly inserted row.
    static func apply(
        entries: [ConfirmEntry],
        existing: [InventoryItem],
        context: ModelContext,
        photoID: String
    ) {
        let stock = existing.map {
            InventoryLogic.StockRef(key: $0.mergeKey, quantity: $0.quantity)
        }
        let confirming = entries.map {
            (key: InventoryLogic.mergeKey(name: $0.name, brand: $0.brand), quantity: 1)
        }
        let actions = InventoryLogic.confirmPlan(confirming: confirming, existing: stock)

        // Rows created by an earlier action in THIS batch. The @Query snapshot in
        // `existing` cannot see them, and an increment for a just-inserted key
        // must still land.
        var insertedThisBatch: [String: InventoryItem] = [:]

        for (index, action) in actions.enumerated() {
            switch action {
            case let .increment(key, newQuantity):
                if let match = existing.first(where: { $0.mergeKey == key }) {
                    match.quantity = newQuantity
                } else if let fresh = insertedThisBatch[key] {
                    fresh.quantity = newQuantity
                }
                // No match in either place means the row was skipped as blank
                // below, so there is nothing to bump.

            case let .insert(key, quantity):
                let entry = entries[index]
                let trimmed = entry.name.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !trimmed.isEmpty else { continue }
                let item = InventoryItem(
                    name: trimmed,
                    brand: (entry.brand?.isEmpty ?? true) ? nil : entry.brand,
                    category: entry.category,
                    quantity: quantity,
                    sourcePhotoID: photoID
                )
                context.insert(item)
                insertedThisBatch[key] = item
            }
        }
    }
}
