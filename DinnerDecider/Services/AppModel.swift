import Foundation
import SwiftUI
import UIKit

/// Where the scan pipeline currently is, for the Scanning screen.
enum ScanPhase: Equatable {
    case idle
    case cropping
    case identifying(done: Int, total: Int)
    case finished
}

/// The single source of truth the UI observes. It owns the `LLMService` behind
/// the protocol so the real llama.cpp runtime can replace `MockLLMService`
/// without any view changes.
@MainActor
final class AppModel: ObservableObject {

    let llm: LLMService

    @Published var isModelLoaded = false
    @Published var isLoadingModel = false

    // Scan state
    @Published var scanPhase: ScanPhase = .idle
    @Published var scannedItems: [IdentifiedItem] = []

    // Recipe state
    @Published var makeNow: [RecipeSuggestion] = []
    @Published var almostThere: [RecipeSuggestion] = []
    @Published var isGeneratingRecipes = false

    init(llm: LLMService = MockLLMService()) {
        self.llm = llm
    }

    /// Aggregated, de-duplicated shopping list from the "almost there" recipes.
    var shoppingList: [String] {
        var seen = Set<String>()
        var list: [String] = []
        for recipe in almostThere {
            for item in recipe.missingItems {
                let key = item.lowercased()
                if !seen.contains(key) {
                    seen.insert(key)
                    list.append(item)
                }
            }
        }
        return list
    }

    // MARK: - Model loading

    func loadModelIfNeeded() async {
        guard !isModelLoaded, !isLoadingModel else { return }
        isLoadingModel = true
        try? await llm.loadModel()
        isModelLoaded = llm.isLoaded
        isLoadingModel = false
    }

    // MARK: - Scan pipeline

    /// Run the crop + OCR + identify pipeline over one photo, streaming results
    /// into `scannedItems` as they arrive.
    func scan(image: UIImage) async {
        scannedItems = []
        scanPhase = .cropping
        await loadModelIfNeeded()

        let cropResult = await Task.detached { CropService.crops(from: image) }.value
        let total = max(cropResult.images.count, 1)

        var results: [IdentifiedItem] = []
        for (index, cropped) in cropResult.images.enumerated() {
            scanPhase = .identifying(done: index, total: total)
            let ocrText = await Task.detached { OCRService.recognizeText(in: cropped) }.value
            if let identified = try? await llm.identifyItem(image: cropped, ocrText: ocrText) {
                results.append(identified)
                scannedItems = dedupe(results)
            }
        }
        scanPhase = .finished
    }

    /// Merge duplicate identifications (same name + brand across overlapping crops).
    private func dedupe(_ items: [IdentifiedItem]) -> [IdentifiedItem] {
        var seen = Set<String>()
        var unique: [IdentifiedItem] = []
        for item in items {
            let key = "\(item.name.lowercased())|\(item.brand?.lowercased() ?? "")"
            if !seen.contains(key) {
                seen.insert(key)
                unique.append(item)
            }
        }
        return unique
    }

    func resetScan() {
        scannedItems = []
        scanPhase = .idle
    }

    // MARK: - Recipes

    /// Ask the model for recipes from the current inventory and preferences.
    func generateRecipes(from items: [InventoryItem]) async {
        guard !isGeneratingRecipes else { return }
        isGeneratingRecipes = true
        await loadModelIfNeeded()

        let prompt = Self.recipePrompt(for: items)
        if let text = try? await llm.generateText(prompt: prompt),
           let bundle = LLMResponseParser.decode(RecipeBundleResponse.self, from: text) {
            makeNow = bundle.makeNow
            almostThere = bundle.almostThere
        }
        isGeneratingRecipes = false
    }

    /// Build a compact prompt: inventory as a comma list, not prose (per spec).
    static func recipePrompt(for items: [InventoryItem]) -> String {
        let defaults = UserDefaults.standard
        let inventory = items.map(\.name).joined(separator: ", ")
        let diet = defaults.string(forKey: PrefKey.diet) ?? DietPreference.none.rawValue
        let allergies = defaults.string(forKey: PrefKey.allergies) ?? ""
        let cuisines = defaults.string(forKey: PrefKey.cuisineLikes) ?? ""

        var lines: [String] = []
        lines.append("You are a helpful home cooking assistant.")
        lines.append("Inventory: \(inventory).")
        if diet != DietPreference.none.rawValue {
            lines.append("Diet: \(diet).")
        }
        if !allergies.trimmingCharacters(in: .whitespaces).isEmpty {
            lines.append("Avoid (allergies): \(allergies).")
        }
        if !cuisines.trimmingCharacters(in: .whitespaces).isEmpty {
            lines.append("Preferred cuisines: \(cuisines).")
        }
        lines.append(
            "Respond ONLY with JSON of the form "
                + "{\"makeNow\":[{\"name\":\"\",\"ingredients\":[{\"name\":\"\",\"hasIt\":true}],"
                + "\"steps\":[\"\"],\"timeMinutes\":0,\"missingItems\":[]}],\"almostThere\":[...]}. "
                + "makeNow uses only inventory items. almostThere needs at most 2 extra items listed in missingItems."
        )
        return lines.joined(separator: "\n")
    }
}
