import SwiftData
import SwiftUI

/// The confirmed inventory. Search, sort, bulk delete, quantity steppers,
/// category re-assignment, and a collapsed "staples" section.
struct InventoryView: View {
    @Environment(\.modelContext) private var modelContext
    @Query private var items: [InventoryItem]

    @AppStorage(PrefKey.inventorySort) private var sortRaw = InventorySort.category.rawValue

    @State private var search = ""
    @State private var editingItem: InventoryItem?
    @State private var showAdd = false
    @State private var selection = Set<PersistentIdentifier>()
    @State private var editMode: EditMode = .inactive
    @State private var staplesExpanded = false
    @State private var showBulkDeleteConfirm = false

    private var sort: InventorySort {
        InventorySort(rawValue: sortRaw) ?? .category
    }

    // MARK: - Derived data

    private var matching: [InventoryItem] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return items }
        return items.filter {
            $0.name.lowercased().contains(query)
                || ($0.brand?.lowercased().contains(query) ?? false)
        }
    }

    private var staples: [InventoryItem] {
        matching.filter(\.isStaple).sorted { $0.name < $1.name }
    }

    private var everyday: [InventoryItem] {
        matching.filter { !$0.isStaple }
    }

    private struct CategoryGroup: Identifiable {
        let id: String
        let title: String
        let items: [InventoryItem]
    }

    private var groups: [CategoryGroup] {
        switch sort {
        case .category:
            let dictionary = Dictionary(grouping: everyday) { $0.category }
            return FoodCategory.allCases.compactMap { category in
                guard let group = dictionary[category], !group.isEmpty else { return nil }
                return CategoryGroup(
                    id: category.rawValue,
                    title: category.displayName,
                    items: group.sorted { $0.name < $1.name }
                )
            }
        case .name:
            return [CategoryGroup(id: "all", title: "All items", items: everyday.sorted { $0.name < $1.name })]
        case .dateAdded:
            return [CategoryGroup(id: "all", title: "Newest first", items: everyday.sorted { $0.dateAdded > $1.dateAdded })]
        }
    }

    // MARK: - Body

    var body: some View {
        NavigationStack {
            Group {
                if items.isEmpty {
                    emptyState
                } else if matching.isEmpty {
                    ContentUnavailableView.search(text: search)
                } else {
                    list
                }
            }
            .dinnerSurfaceBackground()
            .navigationTitle("Inventory")
            .searchable(text: $search, prompt: "Search food")
            .toolbar { toolbarContent }
            .environment(\.editMode, $editMode)
            .safeAreaInset(edge: .bottom) { bulkDeleteBar }
            .sheet(item: $editingItem) { item in
                ItemEditorView(item: item)
            }
            .sheet(isPresented: $showAdd) {
                ItemEditorView(item: nil)
            }
        }
    }

    private var list: some View {
        List(selection: $selection) {
            ForEach(groups) { group in
                Section(group.title) {
                    ForEach(group.items) { item in
                        row(for: item)
                            .tag(item.persistentModelID)
                    }
                }
            }

            if !staples.isEmpty {
                Section {
                    DisclosureGroup(isExpanded: $staplesExpanded) {
                        ForEach(staples) { item in
                            row(for: item)
                                .tag(item.persistentModelID)
                        }
                    } label: {
                        Label("Staples (\(staples.count))", systemImage: "star.fill")
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func row(for item: InventoryItem) -> some View {
        InventoryRow(item: item) {
            editingItem = item
        }
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) {
                modelContext.delete(item)
                Haptics.tap()
            } label: {
                Label("Delete", systemImage: "trash")
            }
        }
        .swipeActions(edge: .leading) {
            Button {
                item.isStaple.toggle()
                Haptics.tap()
            } label: {
                Label(item.isStaple ? "Unstaple" : "Staple", systemImage: item.isStaple ? "star.slash" : "star")
            }
            .tint(.brandAccent)
        }
    }

    // MARK: - Empty state

    private var emptyState: some View {
        ContentUnavailableView {
            Label("No food yet", systemImage: "tray")
        } description: {
            Text("Scan a photo on the Scan tab, or add items by hand to get started.")
        } actions: {
            Button {
                showAdd = true
            } label: {
                Label("Add an item", systemImage: "plus")
            }
            .buttonStyle(.borderedProminent)
        }
    }

    // MARK: - Toolbar

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            if !items.isEmpty {
                EditButton()
            }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            Menu {
                Picker("Sort by", selection: $sortRaw) {
                    ForEach(InventorySort.allCases) { option in
                        Label(option.displayName, systemImage: option.symbolName).tag(option.rawValue)
                    }
                }
            } label: {
                Label("Sort", systemImage: "arrow.up.arrow.down")
            }
            .disabled(items.isEmpty)

            Button {
                showAdd = true
            } label: {
                Label("Add item", systemImage: "plus")
            }
        }
    }

    // MARK: - Bulk delete

    @ViewBuilder
    private var bulkDeleteBar: some View {
        if editMode == .active && !selection.isEmpty {
            Button(role: .destructive) {
                showBulkDeleteConfirm = true
            } label: {
                Label("Delete \(selection.count) selected", systemImage: "trash")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            .controlSize(.large)
            .padding()
            .background(.thinMaterial)
            .confirmationDialog(
                "Delete \(selection.count) \(selection.count == 1 ? "item" : "items")?",
                isPresented: $showBulkDeleteConfirm,
                titleVisibility: .visible
            ) {
                Button("Delete \(selection.count) \(selection.count == 1 ? "item" : "items")", role: .destructive) {
                    deleteSelected()
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This removes them from your inventory. You cannot undo this.")
            }
        }
    }

    private func deleteSelected() {
        for id in selection {
            if let item = items.first(where: { $0.persistentModelID == id }) {
                modelContext.delete(item)
            }
        }
        selection.removeAll()
        Haptics.success()
    }
}

// MARK: - Row

private struct InventoryRow: View {
    @Bindable var item: InventoryItem
    var onTap: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Button(action: onTap) {
                HStack(spacing: 12) {
                    Image(systemName: item.category.symbolName)
                        .foregroundStyle(.tint)
                        .frame(width: 28)
                        .accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(item.name)
                            .font(.dmBody)
                            .foregroundStyle(.primary)
                            .lineLimit(2)
                        if let brand = item.brand, !brand.isEmpty {
                            Text(brand)
                                .font(.dmCaption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                    Spacer(minLength: 8)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            Stepper(
                value: Binding(
                    get: { item.quantity },
                    set: { item.quantity = max(1, $0) }
                ),
                in: 1...99
            ) {
                Text("x\(item.quantity)")
                    .font(.subheadline.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .frame(minWidth: 30, alignment: .trailing)
            }
            .labelsHidden()
            .fixedSize()
            .accessibilityLabel("Quantity of \(item.name)")
            .accessibilityValue("\(item.quantity)")
        }
        .accessibilityElement(children: .contain)
    }
}

// MARK: - Item editor (add + edit)

/// Shared sheet for adding a new item or editing an existing one. Passing `nil`
/// means "add"; passing an item means "edit".
struct ItemEditorView: View {
    let item: InventoryItem?

    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss

    @State private var name: String
    @State private var brand: String
    @State private var category: FoodCategory
    @State private var quantity: Int
    @State private var isStaple: Bool
    @State private var showDeleteConfirm = false

    init(item: InventoryItem?) {
        self.item = item
        _name = State(initialValue: item?.name ?? "")
        _brand = State(initialValue: item?.brand ?? "")
        _category = State(initialValue: item?.category ?? .other)
        _quantity = State(initialValue: item?.quantity ?? 1)
        _isStaple = State(initialValue: item?.isStaple ?? false)
    }

    private var isEditing: Bool { item != nil }
    private var trimmedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                Section("Item") {
                    TextField("Name", text: $name)
                    TextField("Brand (optional)", text: $brand)
                }
                Section("Details") {
                    Picker("Category", selection: $category) {
                        ForEach(FoodCategory.allCases) { option in
                            Label(option.displayName, systemImage: option.symbolName).tag(option)
                        }
                    }
                    Stepper("Quantity: \(quantity)", value: $quantity, in: 1...99)
                    Toggle(isOn: $isStaple) {
                        Label("Staple I always have", systemImage: "star")
                    }
                }
                if isEditing {
                    Section {
                        Button(role: .destructive) {
                            showDeleteConfirm = true
                        } label: {
                            Label("Delete item", systemImage: "trash")
                        }
                    }
                }
            }
            .navigationTitle(isEditing ? "Edit item" : "Add item")
            .navigationBarTitleDisplayMode(.inline)
            .confirmationDialog(
                "Delete \(trimmedName.isEmpty ? "this item" : trimmedName)?",
                isPresented: $showDeleteConfirm,
                titleVisibility: .visible
            ) {
                Button("Delete", role: .destructive) {
                    if let item { modelContext.delete(item) }
                    Haptics.success()
                    dismiss()
                }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This removes it from your inventory. You cannot undo this.")
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(isEditing ? "Save" : "Add") { save() }
                        .disabled(trimmedName.isEmpty)
                }
            }
        }
    }

    private func save() {
        let cleanBrand = brand.trimmingCharacters(in: .whitespacesAndNewlines)
        if let item {
            item.name = trimmedName
            item.brand = cleanBrand.isEmpty ? nil : cleanBrand
            item.category = category
            item.quantity = quantity
            item.isStaple = isStaple
        } else {
            let new = InventoryItem(
                name: trimmedName,
                brand: cleanBrand.isEmpty ? nil : cleanBrand,
                category: category,
                quantity: quantity,
                isStaple: isStaple
            )
            modelContext.insert(new)
        }
        Haptics.success()
        dismiss()
    }
}

#Preview {
    InventoryView()
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self], inMemory: true)
}
