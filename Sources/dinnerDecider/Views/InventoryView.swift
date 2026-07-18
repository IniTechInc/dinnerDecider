import SwiftUI
import SwiftData

/// KAN-27: Inventory list grouped by category. KAN-26: backed by SwiftData.
struct InventoryView: View {
    @Environment(\.modelContext) private var modelContext
    @Query(sort: \FoodItem.name) private var allItems: [FoodItem]

    private var grouped: [(FoodCategory, [FoodItem])] {
        let dict = Dictionary(grouping: allItems, by: \.category)
        return FoodCategory.allCases
            .compactMap { cat in
                guard let items = dict[cat], !items.isEmpty else { return nil }
                return (cat, items)
            }
    }

    var body: some View {
        NavigationStack {
            if allItems.isEmpty {
                ContentUnavailableView(
                    "Your Pantry is Empty",
                    systemImage: "refrigerator",
                    description: Text("Scan your fridge or pantry to add items.")
                )
                .navigationTitle("Pantry")
            } else {
                List {
                    ForEach(grouped, id: \.0) { category, items in
                        Section(category.displayName) {
                            ForEach(items) { item in
                                InventoryRowView(item: item)
                            }
                            .onDelete { indexSet in
                                deleteItems(items, at: indexSet)
                            }
                        }
                    }
                }
                .navigationTitle("Pantry")
                .toolbar {
                    EditButton()
                }
            }
        }
    }

    private func deleteItems(_ items: [FoodItem], at offsets: IndexSet) {
        for index in offsets {
            modelContext.delete(items[index])
        }
    }
}

struct InventoryRowView: View {
    let item: FoodItem

    var body: some View {
        HStack {
            Image(systemName: item.category.systemImage)
                .foregroundStyle(.tint)
                .frame(width: 28)
            VStack(alignment: .leading, spacing: 2) {
                Text(item.name)
                if let brand = item.brand {
                    Text(brand)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            if item.quantity > 1 {
                Text("×\(item.quantity)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}
