import SwiftData
import SwiftUI

/// Three segments of value: Make now, Almost there, Shopping list.
struct RecipesView: View {
    @EnvironmentObject private var appModel: AppModel
    @Query(sort: \InventoryItem.name) private var items: [InventoryItem]

    private enum Segment: String, CaseIterable, Identifiable {
        case makeNow = "Make now"
        case almostThere = "Almost there"
        case shopping = "Shopping list"
        var id: String { rawValue }
    }

    @State private var segment: Segment = .makeNow

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                Picker("Segment", selection: $segment) {
                    ForEach(Segment.allCases) { segment in
                        Text(segment.rawValue).tag(segment)
                    }
                }
                .pickerStyle(.segmented)
                .padding()

                content
            }
            .navigationTitle("Recipes")
            .toolbar {
                if segment != .shopping {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button {
                            Task {
                                Haptics.tap()
                                await appModel.generateRecipes(fromItemNames: items.map(\.name))
                            }
                        } label: {
                            if appModel.isGeneratingRecipes {
                                ProgressView()
                            } else {
                                Label(appModel.hasGeneratedRecipes ? "Regenerate" : "Generate", systemImage: "sparkles")
                            }
                        }
                        .disabled(items.isEmpty || appModel.isGeneratingRecipes)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch segment {
        case .makeNow:
            recipeSection(
                recipes: appModel.makeNow,
                emptyTitle: "Ready when you are",
                emptyText: "Tap Generate to see what you can make right now with what you have."
            )
        case .almostThere:
            recipeSection(
                recipes: appModel.almostThere,
                emptyTitle: "A couple of items away",
                emptyText: "Tap Generate to see recipes you could make by buying just one or two more things."
            )
        case .shopping:
            ShoppingListView()
        }
    }

    @ViewBuilder
    private func recipeSection(recipes: [RecipeSuggestion], emptyTitle: String, emptyText: String) -> some View {
        if items.isEmpty {
            ContentUnavailableView {
                Label("Add some food first", systemImage: "fork.knife")
            } description: {
                Text("Scan or add items, then tap Generate to get recipe ideas.")
            }
        } else if appModel.isGeneratingRecipes {
            ThinkingView()
        } else if let error = appModel.recipeError {
            recipeErrorView(error)
        } else if recipes.isEmpty {
            ContentUnavailableView {
                Label(emptyTitle, systemImage: "sparkles")
            } description: {
                Text(emptyText)
            } actions: {
                Button {
                    Task { await appModel.generateRecipes(fromItemNames: items.map(\.name)) }
                } label: {
                    Label("Generate ideas", systemImage: "sparkles")
                }
                .buttonStyle(.borderedProminent)
            }
        } else {
            List(recipes) { recipe in
                NavigationLink {
                    RecipeDetailView(recipe: recipe)
                } label: {
                    RecipeRow(recipe: recipe)
                }
            }
        }
    }

    private func recipeErrorView(_ message: String) -> some View {
        ContentUnavailableView {
            Label("Hmm, that did not work", systemImage: "exclamationmark.triangle")
        } description: {
            Text(message)
        } actions: {
            Button {
                Task {
                    Haptics.tap()
                    await appModel.generateRecipes(fromItemNames: items.map(\.name))
                }
            } label: {
                Label("Try again", systemImage: "arrow.clockwise")
            }
            .buttonStyle(.borderedProminent)
        }
    }
}

// MARK: - Thinking (loading) view

/// Playful rotating copy while Gemma "thinks".
private struct ThinkingView: View {
    private let messages = [
        "Peeking into your pantry...",
        "Tasting a few ideas...",
        "Pairing your ingredients...",
        "Simmering some suggestions...",
        "Plating up options..."
    ]
    @State private var index = 0
    private let timer = Timer.publish(every: 1.6, on: .main, in: .common).autoconnect()

    var body: some View {
        VStack(spacing: 16) {
            Spacer()
            Image(systemName: "sparkles")
                .font(.system(size: 44))
                .foregroundStyle(.tint)
                .symbolEffect(.pulse, options: .repeating)
                .accessibilityHidden(true)
            ProgressView()
            Text(messages[index])
                .font(.headline)
                .foregroundStyle(.secondary)
                .contentTransition(.opacity)
                .id(index)
            Spacer()
        }
        .frame(maxWidth: .infinity)
        .onReceive(timer) { _ in
            withAnimation {
                index = (index + 1) % messages.count
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Thinking up recipes")
    }
}

// MARK: - Recipe row

private struct RecipeRow: View {
    let recipe: RecipeSuggestion

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(recipe.name)
                .font(.headline)
                .lineLimit(2)
            HStack(spacing: 12) {
                Label("\(recipe.timeMinutes) min", systemImage: "clock")
                if !recipe.missingItems.isEmpty {
                    Label(
                        "Buy \(recipe.missingItems.count) \(recipe.missingItems.count == 1 ? "item" : "items")",
                        systemImage: "cart.badge.plus"
                    )
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }
}

// MARK: - Recipe detail

private struct RecipeDetailView: View {
    let recipe: RecipeSuggestion

    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss
    @Query private var inventory: [InventoryItem]
    @Query private var shopping: [ShoppingListItem]

    @State private var checked = Set<String>()
    @State private var showCookConfirm = false
    @State private var addedToShopping = false

    var body: some View {
        List {
            if !recipe.missingItems.isEmpty {
                Section {
                    almostThereBanner
                }
            }

            Section("Ingredients") {
                ForEach(recipe.ingredients) { line in
                    Button {
                        toggle(line.id)
                    } label: {
                        HStack {
                            Image(systemName: checked.contains(line.id) ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(checked.contains(line.id) ? .green : .secondary)
                            Text(line.name)
                                .strikethrough(checked.contains(line.id))
                                .foregroundStyle(line.hasIt ? .primary : .secondary)
                            Spacer()
                            if !line.hasIt {
                                Text("need")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.orange)
                            }
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(line.name)
                    .accessibilityValue(checked.contains(line.id) ? "checked" : "not checked")
                    .accessibilityHint(line.hasIt ? "In your inventory" : "You need to buy this")
                }
            }

            Section("Steps") {
                ForEach(Array(recipe.steps.enumerated()), id: \.offset) { index, step in
                    HStack(alignment: .top, spacing: 10) {
                        Text("\(index + 1)")
                            .font(.subheadline.bold())
                            .foregroundStyle(.white)
                            .frame(width: 24, height: 24)
                            .background(Circle().fill(.tint))
                            .accessibilityHidden(true)
                        Text(step)
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("Step \(index + 1). \(step)")
                }
            }

            Section {
                Button {
                    Haptics.tap()
                    showCookConfirm = true
                } label: {
                    Label("Mark as cooked", systemImage: "checkmark.seal")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
            } footer: {
                Text("Cooking will lower the quantity of the ingredients you used.")
            }
        }
        .navigationTitle(recipe.name)
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog(
            "Mark this as cooked?",
            isPresented: $showCookConfirm,
            titleVisibility: .visible
        ) {
            Button("Cook it") { markCooked() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("We will lower the amount of each ingredient you had on hand.")
        }
    }

    private var almostThereBanner: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(missingSentence, systemImage: "cart.badge.plus")
                .font(.subheadline.weight(.medium))
            Button {
                addMissingToShopping()
            } label: {
                Label(addedToShopping ? "Added to shopping list" : "Add to shopping list", systemImage: addedToShopping ? "checkmark" : "plus")
            }
            .buttonStyle(.bordered)
            .disabled(addedToShopping)
        }
        .padding(.vertical, 4)
    }

    private var missingSentence: String {
        let items = recipe.missingItems
        let joined: String
        if items.count == 1 {
            joined = items[0]
        } else if items.count == 2 {
            joined = "\(items[0]) and \(items[1])"
        } else {
            joined = items.dropLast().joined(separator: ", ") + ", and " + (items.last ?? "")
        }
        return "Buy \(joined) to make this."
    }

    private func toggle(_ id: String) {
        if checked.contains(id) {
            checked.remove(id)
        } else {
            checked.insert(id)
            Haptics.select()
        }
    }

    private func addMissingToShopping() {
        let existing = shopping.map(\.name)
        let toAdd = ShoppingListLogic.newItems(adding: recipe.missingItems, existing: existing)
        for name in toAdd {
            modelContext.insert(ShoppingListItem(name: name, isManual: false))
        }
        addedToShopping = true
        Haptics.success()
    }

    private func markCooked() {
        let usedKeys = recipe.ingredients
            .filter(\.hasIt)
            .map { InventoryLogic.nameKey($0.name) }
        let stock = inventory.map { InventoryLogic.StockRef(key: InventoryLogic.nameKey($0.name), quantity: $0.quantity) }
        let changes = InventoryLogic.cookPlan(usedKeys: usedKeys, stock: stock)

        for change in changes {
            guard let item = inventory.first(where: { InventoryLogic.nameKey($0.name) == change.key }) else { continue }
            if change.removed {
                modelContext.delete(item)
            } else {
                item.quantity = change.newQuantity
            }
        }
        Haptics.success()
        dismiss()
    }
}

// MARK: - Shopping list

private struct ShoppingListView: View {
    @Environment(\.modelContext) private var modelContext
    @Query(sort: \ShoppingListItem.dateAdded) private var items: [ShoppingListItem]

    @State private var newItem = ""
    @FocusState private var addFieldFocused: Bool

    private var hasChecked: Bool { items.contains(where: \.isChecked) }

    private var exportLines: [ShoppingListLogic.Line] {
        items.map { ShoppingListLogic.Line(name: $0.name, isChecked: $0.isChecked) }
    }

    var body: some View {
        VStack(spacing: 0) {
            addRow

            if items.isEmpty {
                ContentUnavailableView {
                    Label("Shopping list is empty", systemImage: "cart")
                } description: {
                    Text("Add items above, or open an 'Almost there' recipe and add what you need.")
                }
            } else {
                List {
                    ForEach(items) { item in
                        row(for: item)
                    }
                    .onDelete(perform: delete)
                }
            }
        }
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                if !items.isEmpty {
                    ShareLink(item: ShoppingListLogic.exportText(exportLines)) {
                        Label("Share", systemImage: "square.and.arrow.up")
                    }
                }
                if hasChecked {
                    Button {
                        clearChecked()
                    } label: {
                        Label("Clear checked", systemImage: "checklist.checked")
                    }
                }
            }
        }
    }

    private var addRow: some View {
        HStack {
            TextField("Add an item", text: $newItem)
                .focused($addFieldFocused)
                .submitLabel(.done)
                .onSubmit(add)
            Button(action: add) {
                Image(systemName: "plus.circle.fill")
                    .font(.title2)
            }
            .disabled(newItem.trimmingCharacters(in: .whitespaces).isEmpty)
            .accessibilityLabel("Add to shopping list")
        }
        .padding(.horizontal)
        .padding(.bottom, 8)
    }

    private func row(for item: ShoppingListItem) -> some View {
        Button {
            item.isChecked.toggle()
            Haptics.select()
        } label: {
            HStack {
                Image(systemName: item.isChecked ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(item.isChecked ? .green : .secondary)
                Text(item.name)
                    .strikethrough(item.isChecked)
                    .foregroundStyle(item.isChecked ? .secondary : .primary)
                Spacer()
                if item.isManual {
                    Image(systemName: "pencil")
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                        .accessibilityHidden(true)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(item.name)
        .accessibilityValue(item.isChecked ? "checked" : "not checked")
    }

    private func add() {
        let name = newItem.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { return }
        let existing = items.map(\.name)
        if let toAdd = ShoppingListLogic.newItems(adding: [name], existing: existing).first {
            modelContext.insert(ShoppingListItem(name: toAdd, isManual: true))
            Haptics.success()
        }
        newItem = ""
    }

    private func delete(_ offsets: IndexSet) {
        for index in offsets {
            modelContext.delete(items[index])
        }
    }

    private func clearChecked() {
        for item in items where item.isChecked {
            modelContext.delete(item)
        }
        Haptics.tap()
    }
}

#Preview {
    RecipesView()
        .environmentObject(AppModel())
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self], inMemory: true)
}
