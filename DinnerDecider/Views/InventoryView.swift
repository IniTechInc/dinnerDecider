import SwiftData
import SwiftUI

/// The confirmed inventory, grouped by category. Swipe to delete, tap to rename,
/// plus a manual add button.
struct InventoryView: View {
    @Environment(\.modelContext) private var modelContext
    @Query(sort: \InventoryItem.name) private var items: [InventoryItem]

    @State private var renamingItem: InventoryItem?
    @State private var renameText = ""
    @State private var showAdd = false

    private var grouped: [(category: FoodCategory, items: [InventoryItem])] {
        let dictionary = Dictionary(grouping: items) { $0.category }
        return FoodCategory.allCases.compactMap { category in
            guard let group = dictionary[category], !group.isEmpty else { return nil }
            return (category, group.sorted { $0.name < $1.name })
        }
    }

    var body: some View {
        NavigationStack {
            Group {
                if items.isEmpty {
                    ContentUnavailableView(
                        "No food yet",
                        systemImage: "tray",
                        description: Text("Scan a photo on the Scan tab, or add items by hand.")
                    )
                } else {
                    List {
                        ForEach(grouped, id: \.category) { group in
                            Section(group.category.displayName) {
                                ForEach(group.items) { item in
                                    row(for: item)
                                }
                                .onDelete { offsets in
                                    delete(offsets, in: group.items)
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Inventory")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showAdd = true
                    } label: {
                        Image(systemName: "plus")
                    }
                }
            }
            .alert("Rename item", isPresented: renameBinding) {
                TextField("Name", text: $renameText)
                Button("Save") {
                    if let item = renamingItem {
                        item.name = renameText.trimmingCharacters(in: .whitespaces)
                    }
                    renamingItem = nil
                }
                Button("Cancel", role: .cancel) {
                    renamingItem = nil
                }
            }
            .sheet(isPresented: $showAdd) {
                AddItemView()
            }
        }
    }

    private func row(for item: InventoryItem) -> some View {
        Button {
            renamingItem = item
            renameText = item.name
        } label: {
            HStack {
                Image(systemName: item.category.symbolName)
                    .foregroundStyle(.tint)
                    .frame(width: 28)
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.name)
                        .foregroundStyle(.primary)
                    if let brand = item.brand, !brand.isEmpty {
                        Text(brand)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if item.quantity > 1 {
                    Text("x\(item.quantity)")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
            }
        }
        .buttonStyle(.plain)
    }

    private var renameBinding: Binding<Bool> {
        Binding(
            get: { renamingItem != nil },
            set: { if !$0 { renamingItem = nil } }
        )
    }

    private func delete(_ offsets: IndexSet, in group: [InventoryItem]) {
        for index in offsets {
            modelContext.delete(group[index])
        }
    }
}

/// A small sheet for manually adding an item.
private struct AddItemView: View {
    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var brand = ""
    @State private var category: FoodCategory = .other
    @State private var quantity = 1

    var body: some View {
        NavigationStack {
            Form {
                Section("Item") {
                    TextField("Name", text: $name)
                    TextField("Brand (optional)", text: $brand)
                }
                Section("Details") {
                    Picker("Category", selection: $category) {
                        ForEach(FoodCategory.allCases) { category in
                            Text(category.displayName).tag(category)
                        }
                    }
                    Stepper("Quantity: \(quantity)", value: $quantity, in: 1...99)
                }
            }
            .navigationTitle("Add Item")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        let item = InventoryItem(
                            name: name.trimmingCharacters(in: .whitespaces),
                            brand: brand.isEmpty ? nil : brand,
                            category: category,
                            quantity: quantity
                        )
                        modelContext.insert(item)
                        dismiss()
                    }
                    .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
    }
}

#Preview {
    InventoryView()
        .modelContainer(for: InventoryItem.self, inMemory: true)
}
