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
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await appModel.generateRecipes(from: items) }
                    } label: {
                        if appModel.isGeneratingRecipes {
                            ProgressView()
                        } else {
                            Label("Generate", systemImage: "sparkles")
                        }
                    }
                    .disabled(items.isEmpty || appModel.isGeneratingRecipes)
                }
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if items.isEmpty {
            ContentUnavailableView(
                "Add some food first",
                systemImage: "fork.knife",
                description: Text("Scan or add items, then tap Generate to get recipe ideas.")
            )
        } else {
            switch segment {
            case .makeNow:
                recipeList(appModel.makeNow, emptyText: "Tap Generate to see what you can make right now.")
            case .almostThere:
                recipeList(appModel.almostThere, emptyText: "Tap Generate to see recipes that need just a couple more items.")
            case .shopping:
                shoppingList
            }
        }
    }

    @ViewBuilder
    private func recipeList(_ recipes: [RecipeSuggestion], emptyText: String) -> some View {
        if recipes.isEmpty {
            ContentUnavailableView(
                "Nothing here yet",
                systemImage: "sparkles",
                description: Text(emptyText)
            )
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

    @ViewBuilder
    private var shoppingList: some View {
        let list = appModel.shoppingList
        if list.isEmpty {
            ContentUnavailableView(
                "Shopping list is empty",
                systemImage: "cart",
                description: Text("Generate 'Almost there' recipes and the items you need will collect here.")
            )
        } else {
            List(list, id: \.self) { item in
                Label(item, systemImage: "cart")
            }
        }
    }
}

private struct RecipeRow: View {
    let recipe: RecipeSuggestion

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(recipe.name)
                .font(.headline)
            HStack(spacing: 12) {
                Label("\(recipe.timeMinutes) min", systemImage: "clock")
                if !recipe.missingItems.isEmpty {
                    Label("Buy \(recipe.missingItems.count)", systemImage: "cart.badge.plus")
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
    }
}

private struct RecipeDetailView: View {
    let recipe: RecipeSuggestion

    var body: some View {
        List {
            Section("Ingredients") {
                ForEach(recipe.ingredients) { line in
                    HStack {
                        Image(systemName: line.hasIt ? "checkmark.circle.fill" : "circle")
                            .foregroundStyle(line.hasIt ? .green : .secondary)
                        Text(line.name)
                    }
                }
            }
            Section("Steps") {
                ForEach(Array(recipe.steps.enumerated()), id: \.offset) { index, step in
                    HStack(alignment: .top, spacing: 8) {
                        Text("\(index + 1).")
                            .foregroundStyle(.secondary)
                        Text(step)
                    }
                }
            }
            if !recipe.missingItems.isEmpty {
                Section("You still need") {
                    ForEach(recipe.missingItems, id: \.self) { item in
                        Label(item, systemImage: "cart")
                    }
                }
            }
        }
        .navigationTitle(recipe.name)
        .navigationBarTitleDisplayMode(.inline)
    }
}

#Preview {
    RecipesView()
        .environmentObject(AppModel())
        .modelContainer(for: InventoryItem.self, inMemory: true)
}
