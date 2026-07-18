import Foundation
import SwiftUI
import UIKit

/// Progress detail while a scan is running, across one or more photos.
struct ScanProgress: Equatable {
    enum Stage: Equatable {
        case cropping
        case identifying
    }

    /// Zero-based index of the photo currently being processed.
    var photoIndex: Int
    /// Total number of photos in this batch.
    var photoCount: Int
    /// Items identified so far in the current photo.
    var itemsDone: Int
    /// Items expected in the current photo (crops found).
    var itemsTotal: Int
    var stage: Stage
}

/// Where the scan pipeline currently is, for the Scanning screen.
enum ScanPhase: Equatable {
    case idle
    case scanning(ScanProgress)
    /// Finished with at least one item found.
    case finished
    /// Finished but nothing recognisable was spotted.
    case empty
    /// The user cancelled part way through.
    case cancelled
}

/// The single source of truth the UI observes. It owns the `LLMService` behind
/// the protocol so the real llama.cpp runtime can replace `MockLLMService`
/// without any view changes.
@MainActor
final class AppModel: ObservableObject {

    let llm: LLMService

    @Published var isModelLoaded = false
    @Published var isLoadingModel = false

    /// True when the app is running on the canned `MockLLMService` (no model
    /// files on the device) rather than the real on-device Gemma 4 runtime. The
    /// UI surfaces this as a small badge so it is always obvious which mode is
    /// active (real on-device AI vs. sample data for simulator/dev).
    @Published private(set) var isUsingMock: Bool

    // Scan state
    @Published var scanPhase: ScanPhase = .idle
    @Published var scannedItems: [IdentifiedItem] = []
    private var scanTask: Task<Void, Never>?

    // Recipe state
    @Published var makeNow: [RecipeSuggestion] = []
    @Published var almostThere: [RecipeSuggestion] = []
    @Published var isGeneratingRecipes = false
    /// Set when the model reply could not be parsed, driving the retry UI.
    @Published var recipeError: String?
    /// True once at least one successful recipe generation has completed, so the
    /// UI can tell "never generated" apart from "generated, nothing matched".
    @Published var hasGeneratedRecipes = false
    private var recipeTask: Task<Void, Never>?

    private let defaults: UserDefaults

    /// Uses the real on-device Gemma 4 service when the model files are present,
    /// and automatically falls back to `MockLLMService` otherwise (e.g. in the
    /// simulator or before the model is installed) so the app stays fully
    /// navigable. Pass an explicit service to override (tests, previews).
    init(llm: LLMService? = nil, defaults: UserDefaults = .standard) {
        let resolved = llm ?? AppModel.makeDefaultService()
        self.llm = resolved
        self.isUsingMock = resolved is MockLLMService
        self.defaults = defaults
    }

    /// Real service if the model is on the device, mock otherwise.
    static func makeDefaultService() -> LLMService {
        ModelFileLocator().isModelPresent ? GemmaLLMService() : MockLLMService()
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

    /// Kick off the crop + OCR + identify pipeline over one or more photos
    /// (fridge, pantry, shelves in a single session). Owned by the model rather
    /// than a view so navigation or backgrounding never tears it down; results
    /// stream into `scannedItems` and the view simply reflects them.
    func startScan(images: [UIImage]) {
        scanTask?.cancel()
        scannedItems = []
        let photos = images.filter { $0.cgImage != nil || $0.ciImage != nil }
        guard !photos.isEmpty else {
            scanPhase = .empty
            return
        }
        scanPhase = .scanning(
            ScanProgress(photoIndex: 0, photoCount: photos.count, itemsDone: 0, itemsTotal: 0, stage: .cropping)
        )
        scanTask = Task { [weak self] in
            await self?.runScan(photos)
        }
    }

    private func runScan(_ images: [UIImage]) async {
        await loadModelIfNeeded()
        var results: [IdentifiedItem] = []

        for (photoIndex, image) in images.enumerated() {
            if Task.isCancelled { return }
            scanPhase = .scanning(
                ScanProgress(
                    photoIndex: photoIndex,
                    photoCount: images.count,
                    itemsDone: 0,
                    itemsTotal: 0,
                    stage: .cropping
                )
            )

            let cropResult = await Task.detached { CropService.crops(from: image) }.value
            let total = cropResult.images.count

            for (index, cropped) in cropResult.images.enumerated() {
                if Task.isCancelled { return }
                scanPhase = .scanning(
                    ScanProgress(
                        photoIndex: photoIndex,
                        photoCount: images.count,
                        itemsDone: index,
                        itemsTotal: total,
                        stage: .identifying
                    )
                )
                let ocrText = await Task.detached { OCRService.recognizeText(in: cropped) }.value
                if let identified = try? await llm.identifyItem(image: cropped, ocrText: ocrText) {
                    results.append(identified)
                    // Gentle stream-in spring, but honour Reduce Motion.
                    let reduceMotion = UIAccessibility.isReduceMotionEnabled
                    withMotion(reduceMotion, .spring(response: 0.35, dampingFraction: 0.7)) {
                        scannedItems = InventoryLogic.dedupe(results)
                    }
                }
            }
        }

        if Task.isCancelled { return }
        scanPhase = scannedItems.isEmpty ? .empty : .finished
    }

    /// Stop an in-progress scan (user tapped Cancel).
    func cancelScan() {
        scanTask?.cancel()
        scanTask = nil
        scanPhase = .cancelled
    }

    func resetScan() {
        scanTask?.cancel()
        scanTask = nil
        scannedItems = []
        scanPhase = .idle
    }

    // MARK: - Recipes

    /// Kick off recipe generation in a cancellable task so the loading state can
    /// always be backed out of (see `cancelRecipeGeneration`).
    func requestRecipes(fromItemNames names: [String]) {
        guard !isGeneratingRecipes else { return }
        recipeTask?.cancel()
        recipeTask = Task { [weak self] in
            await self?.generateRecipes(fromItemNames: names)
        }
    }

    /// Stop an in-progress recipe generation and return to the previous state.
    func cancelRecipeGeneration() {
        recipeTask?.cancel()
        recipeTask = nil
        isGeneratingRecipes = false
    }

    /// Ask the model for recipes from the current inventory and preferences.
    /// Surfaces `recipeError` when the reply is unreadable so the UI can retry.
    func generateRecipes(fromItemNames names: [String]) async {
        guard !isGeneratingRecipes else { return }
        recipeError = nil
        isGeneratingRecipes = true
        await loadModelIfNeeded()

        let prompt = Self.recipePrompt(itemNames: names, prefs: RecipePreferences.current(defaults))
        let raw: String
        if defaults.bool(forKey: PrefKey.debugSimulateFailure) {
            // Hidden debug switch: return a deliberately broken reply so the
            // real error-and-retry UI can be demonstrated end to end.
            raw = "Sure! Here are some ideas: { this response is not valid json at all"
        } else {
            raw = (try? await llm.generateText(prompt: prompt)) ?? ""
        }

        // User cancelled while the model was thinking: leave state untouched.
        if Task.isCancelled {
            isGeneratingRecipes = false
            return
        }

        if let bundle = LLMResponseParser.decode(RecipeBundleResponse.self, from: raw) {
            makeNow = bundle.makeNow
            almostThere = bundle.almostThere
            hasGeneratedRecipes = true
            recipeError = nil
        } else {
            recipeError = "We could not read the recipe ideas this time. Please try again."
        }
        isGeneratingRecipes = false
    }

    /// Build a compact prompt: inventory as a comma list, not prose (per spec).
    /// Pure and testable: pass names + a preferences snapshot, no globals.
    nonisolated static func recipePrompt(itemNames: [String], prefs: RecipePreferences) -> String {
        let inventory = itemNames.joined(separator: ", ")

        var lines: [String] = []
        lines.append("You are a helpful home cooking assistant.")
        lines.append("Inventory: \(inventory).")
        if prefs.diet != DietPreference.none.rawValue {
            lines.append("Diet: \(prefs.diet).")
        }
        if !prefs.allergies.trimmingCharacters(in: .whitespaces).isEmpty {
            lines.append("Avoid (allergies): \(prefs.allergies).")
        }
        if !prefs.cuisines.trimmingCharacters(in: .whitespaces).isEmpty {
            lines.append("Preferred cuisines: \(prefs.cuisines).")
        }
        if let portion = InventoryLogic.portionLine(householdSize: prefs.householdSize) {
            lines.append(portion)
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
