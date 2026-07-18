import SwiftUI
import SwiftData

/// KAN-28 / KAN-29: Make Now + Almost There tabs.
struct RecipesView: View {
    @Query private var inventory: [FoodItem]
    @State private var selectedTab = 0

    var body: some View {
        NavigationStack {
            if inventory.isEmpty {
                // KAN-42: Empty state
                ContentUnavailableView(
                    "No Ingredients Found",
                    systemImage: "fork.knife",
                    description: Text("Scan your fridge or pantry first.")
                )
                .navigationTitle("Recipes")
            } else {
                VStack(spacing: 0) {
                    Picker("Recipe Type", selection: $selectedTab) {
                        Text("Make Now").tag(0)
                        Text("Almost There").tag(1)
                    }
                    .pickerStyle(.segmented)
                    .padding()

                    if selectedTab == 0 {
                        MakeNowView(inventory: inventory)
                    } else {
                        AlmostThereView(inventory: inventory)
                    }
                }
                .navigationTitle("Recipes")
            }
        }
    }
}

struct MakeNowView: View {
    let inventory: [FoodItem]
    @State private var recipes: [Recipe] = []
    @State private var isLoading = false

    var body: some View {
        Group {
            if isLoading {
                VStack(spacing: 16) {
                    Spacer()
                    ProgressView()
                    Text("Generating recipes…")
                        .foregroundStyle(.secondary)
                    Spacer()
                }
            } else if recipes.isEmpty {
                Button("Generate Recipes") {
                    Task { await generateRecipes() }
                }
                .buttonStyle(.borderedProminent)
                .padding()
                Spacer()
            } else {
                List(recipes) { recipe in
                    RecipeRowView(recipe: recipe)
                }
            }
        }
        .task {
            if recipes.isEmpty { await generateRecipes() }
        }
    }

    private func generateRecipes() async {
        isLoading = true
        defer { isLoading = false }

        guard ModelService.shared.isLoaded else {
            recipes = Recipe.placeholders
            return
        }

        let itemList = inventory.prefix(20).map { item in
            item.brand != nil ? "\(item.name) (\(item.brand!))" : item.name
        }.joined(separator: ", ")

        let system = """
        You are a chef. Respond ONLY with a JSON array:
        [{"name":"string","ingredients":[{"name":"string","have":true}],"steps":["string"],"timeMinutes":number,"missingItems":[]}]
        """
        let user = "Suggest 3 recipes I can make NOW with: \(itemList). Use only what I have."

        do {
            let response = try await ModelService.shared.generateText(system: system, user: user)
            if let parsed = parseRecipes(response) {
                recipes = parsed
            } else {
                recipes = Recipe.placeholders
            }
        } catch {
            recipes = Recipe.placeholders
        }
    }

    private func parseRecipes(_ json: String) -> [Recipe]? {
        guard let start = json.firstIndex(of: "["),
              let end = json.lastIndex(of: "]"),
              start <= end else { return nil }
        let arrayString = String(json[start...end])
        guard let data = arrayString.data(using: .utf8) else { return nil }

        struct RecipeOutput: Decodable {
            struct IngredientOutput: Decodable { let name: String; let have: Bool }
            let name: String
            let ingredients: [IngredientOutput]
            let steps: [String]
            let timeMinutes: Int
            let missingItems: [String]?
        }

        guard let outputs = try? JSONDecoder().decode([RecipeOutput].self, from: data) else { return nil }
        return outputs.map { output in
            Recipe(
                name: output.name,
                ingredients: output.ingredients.map { RecipeIngredient(name: $0.name, have: $0.have) },
                steps: output.steps,
                timeMinutes: output.timeMinutes,
                missingItems: output.missingItems ?? []
            )
        }
    }
}

struct AlmostThereView: View {
    let inventory: [FoodItem]

    var body: some View {
        List(Recipe.almostTherePlaceholders) { recipe in
            RecipeRowView(recipe: recipe, showMissingItems: true)
        }
    }
}

// MARK: - Recipe model (transient, not persisted)

struct Recipe: Identifiable {
    let id = UUID()
    let name: String
    let ingredients: [RecipeIngredient]
    let steps: [String]
    let timeMinutes: Int
    var missingItems: [String] = []

    static let placeholders: [Recipe] = [
        Recipe(
            name: "Scrambled Eggs with Cheddar",
            ingredients: [
                RecipeIngredient(name: "Eggs", have: true),
                RecipeIngredient(name: "Cheddar Cheese", have: true),
                RecipeIngredient(name: "Butter", have: false),
            ],
            steps: ["Beat eggs in a bowl.", "Melt butter in a pan.", "Add eggs and stir until cooked.", "Top with cheese."],
            timeMinutes: 10
        ),
        Recipe(
            name: "Chicken Quesadilla",
            ingredients: [
                RecipeIngredient(name: "Chicken Breast", have: true),
                RecipeIngredient(name: "Cheddar Cheese", have: true),
                RecipeIngredient(name: "Flour Tortillas", have: false),
            ],
            steps: ["Cook chicken.", "Place in tortilla with cheese.", "Grill until crispy."],
            timeMinutes: 20
        ),
    ]

    static let almostTherePlaceholders: [Recipe] = [
        Recipe(
            name: "Chicken Alfredo",
            ingredients: [],
            steps: [],
            timeMinutes: 30,
            missingItems: ["Pasta", "Heavy cream"]
        ),
    ]
}

struct RecipeIngredient: Identifiable {
    let id = UUID()
    let name: String
    let have: Bool
}

struct RecipeRowView: View {
    let recipe: Recipe
    var showMissingItems = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(recipe.name)
                    .font(.headline)
                Spacer()
                Label("\(recipe.timeMinutes)m", systemImage: "clock")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if showMissingItems && !recipe.missingItems.isEmpty {
                Text("Buy: \(recipe.missingItems.joined(separator: ", "))")
                    .font(.caption)
                    .foregroundStyle(.orange)
            } else {
                let haveCount = recipe.ingredients.filter(\.have).count
                Text("\(haveCount)/\(recipe.ingredients.count) ingredients on hand")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
    }
}
