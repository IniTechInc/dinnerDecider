import SwiftUI
import SwiftData

/// KAN-25: Confirm/edit screen after scanning — rename, delete, or add items before persisting.
struct InventoryConfirmView: View {
    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss

    @State var scannedItems: [ScannedItem]
    @State private var showAddItem = false
    @State private var newItemName = ""
    @State private var saved = false

    var body: some View {
        List {
            Section("Detected Items") {
                ForEach($scannedItems) { $item in
                    HStack {
                        Image(systemName: item.category.systemImage)
                            .foregroundStyle(.tint)
                            .frame(width: 28)
                        VStack(alignment: .leading, spacing: 2) {
                            TextField("Item name", text: $item.name)
                                .font(.body)
                            if let brand = item.brand {
                                Text(brand)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                .onDelete { indexSet in
                    scannedItems.remove(atOffsets: indexSet)
                }
            }

            Section {
                Button {
                    showAddItem = true
                } label: {
                    Label("Add Item Manually", systemImage: "plus")
                }
            }
        }
        .navigationTitle("Confirm Items")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Save to Pantry") {
                    saveItems()
                }
                .disabled(scannedItems.isEmpty)
            }
        }
        .alert("Add Item", isPresented: $showAddItem) {
            TextField("Item name", text: $newItemName)
            Button("Add") {
                if !newItemName.trimmingCharacters(in: .whitespaces).isEmpty {
                    scannedItems.append(ScannedItem(name: newItemName, confidence: 1.0))
                    newItemName = ""
                }
            }
            Button("Cancel", role: .cancel) { newItemName = "" }
        }
        .navigationDestination(isPresented: $saved) {
            InventoryView()
        }
    }

    private func saveItems() {
        for item in scannedItems {
            let foodItem = FoodItem(
                name: item.name,
                brand: item.brand,
                category: item.category,
                quantity: 1
            )
            modelContext.insert(foodItem)
        }
        try? modelContext.save()
        saved = true
    }
}
