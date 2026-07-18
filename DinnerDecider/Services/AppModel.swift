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

/// Which model-load moment the UI is in, so a warm loading treatment can appear
/// before per-item progress does. Derived from `isLoadingModel` +
/// `didReleaseModelForMemory` so it stays a single source of truth.
enum ModelLoadPhase: Equatable {
    /// Not loading (either already resident, or no load in flight).
    case idle
    /// Loading into memory for the first time this session (the ~20s wait).
    case firstLoad
    /// Reloading after the model was freed under memory pressure.
    case reloadingAfterMemory
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

    /// True after the resident model was released under memory pressure and has
    /// not yet been reloaded. Lets the scanning UI show a brief "Warming up again"
    /// note instead of a bare spinner when the next scan has to reload the model.
    @Published private(set) var didReleaseModelForMemory = false

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

    /// Free-form mood or craving text from voice or keyboard input, e.g.
    /// "I'm feeling like Italian tonight". Fed into the recipe prompt so the
    /// model tailors its suggestions.
    @Published var moodText: String = ""
    private var recipeTask: Task<Void, Never>?

    private let defaults: UserDefaults

    /// Explicit state machine for the on-device model. It is the single authority
    /// on whether an unload is safe right now: memory-pressure and reload requests
    /// are routed through it so the multi-GB llama client is never freed while a
    /// load or an inference is still in flight (the crash this fixes).
    private var lifecycle = ModelLifecycle()

    /// Token for the UIKit memory-warning observer, removed on deinit.
    private var memoryWarningObserver: NSObjectProtocol?
    /// Low-level memory-pressure source, catching warning/critical events even
    /// when UIKit does not post a notification.
    private var memoryPressureSource: DispatchSourceMemoryPressure?

    /// Uses the real on-device Gemma 4 service when the model files are present,
    /// and automatically falls back to `MockLLMService` otherwise (e.g. in the
    /// simulator or before the model is installed) so the app stays fully
    /// navigable. Pass an explicit service to override (tests, previews).
    init(llm: LLMService? = nil, defaults: UserDefaults = .standard) {
        let resolved = llm ?? AppModel.makeDefaultService()
        self.llm = resolved
        self.isUsingMock = resolved is MockLLMService
        self.defaults = defaults
        // The headless self-test drives its own GemmaLLMService and must run in a
        // process that behaves exactly like the last known-good build: no extra
        // memory-pressure machinery firing a storm of no-op handlers while it
        // loads the model. Only the normal app installs the pressure sources.
        if !SelfTest.isRequested {
            registerForMemoryPressure()
        }
    }

    deinit {
        if let memoryWarningObserver {
            NotificationCenter.default.removeObserver(memoryWarningObserver)
        }
        memoryPressureSource?.cancel()
    }

    // MARK: - Memory pressure

    /// Listen for memory pressure from both UIKit and the kernel. Either path
    /// funnels into `handleMemoryPressure()`, which is idempotent.
    private func registerForMemoryPressure() {
        memoryWarningObserver = NotificationCenter.default.addObserver(
            forName: UIApplication.didReceiveMemoryWarningNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            // UIKit only posts a plain memory warning, never a "critical" tier.
            Task { @MainActor in self?.handleMemoryPressure(.warning) }
        }

        let source = DispatchSource.makeMemoryPressureSource(
            eventMask: [.warning, .critical],
            queue: .main
        )
        source.setEventHandler { [weak self, weak source] in
            let level: MemoryPressureLevel =
                (source?.data.contains(.critical) ?? false) ? .critical : .warning
            Task { @MainActor in self?.handleMemoryPressure(level) }
        }
        source.resume()
        memoryPressureSource = source
    }

    /// React to system memory pressure.
    ///
    /// The lifecycle decides what is safe: a `.warning` while the model is loading
    /// or inferring is IGNORED (those phases legitimately spike wired memory, and
    /// freeing the client mid-operation is the use-after-free that crashed the
    /// app). Freeing the ~3.5GB wired model only happens when it is resident and
    /// idle, or is deferred until an in-flight operation drains. Defaults to
    /// `.warning` so existing callers and tests keep the idle-unload behavior.
    func handleMemoryPressure(_ level: MemoryPressureLevel = .warning) {
        if case .proceed = lifecycle.handleMemoryPressure(level) {
            performUnload()
        }
    }

    /// Actually free the resident client and record that we released it under
    /// pressure (drives the "warming up again" treatment on the next load).
    private func performUnload() {
        llm.unloadModel()
        lifecycle.finishUnload()
        isModelLoaded = false
        didReleaseModelForMemory = true
    }

    /// Honor a unload that was deferred while the model was busy, now that we are
    /// back to idle. Call after any load or inference completes.
    private func drainDeferredUnloadIfNeeded() {
        if case .proceed = lifecycle.drainPendingUnload() {
            performUnload()
        }
    }

    /// Close a scan/recipe session; runs any unload a `.critical` event deferred
    /// while the session was busy.
    private func endModelSession() {
        if case .proceed = lifecycle.endSession() {
            performUnload()
        }
    }

    /// Real service if the model is on the device, mock otherwise.
    /// On the simulator the Metal GPU backend for the mmproj (vision projector)
    /// cannot allocate XPC shared-memory buffers (`MTLSimDevice` limitation),
    /// so the real model always crashes at load time. Force the mock there.
    static func makeDefaultService() -> LLMService {
        #if targetEnvironment(simulator)
        return MockLLMService()
        #else
        return ModelFileLocator().isModelPresent ? GemmaLLMService() : MockLLMService()
        #endif
    }

    // MARK: - Model loading

    /// The current model-load moment, driving the warm loading treatment in the
    /// scanning and recipe screens. `.idle` whenever no load is in flight.
    var modelLoadPhase: ModelLoadPhase {
        guard isLoadingModel else { return .idle }
        return didReleaseModelForMemory ? .reloadingAfterMemory : .firstLoad
    }

    func loadModelIfNeeded() async {
        // Coalesce duplicate loads and skip when already resident.
        switch lifecycle.beginLoad() {
        case .coalesced, .alreadyLoaded:
            return
        case .proceed:
            break
        }
        isLoadingModel = true
        try? await llm.loadModel()
        let loaded = llm.isLoaded
        lifecycle.finishLoad(success: loaded)
        isModelLoaded = loaded
        isLoadingModel = false
        if loaded { didReleaseModelForMemory = false }
        // A `.critical` event or an explicit unload may have arrived mid-load; it
        // was deferred, so honor it now that the client is resident and idle.
        drainDeferredUnloadIfNeeded()
    }

    /// Bracket a single model inference so the lifecycle knows the client is in
    /// use. While `inferring`, memory pressure can only *defer* an unload, never
    /// free the client out from under the running operation. On completion any
    /// deferred unload is honored.
    private func runInference<T>(_ operation: () async throws -> T) async rethrows -> T {
        let started = lifecycle.beginInference()
        defer {
            if started {
                lifecycle.finishInference()
                drainDeferredUnloadIfNeeded()
            }
        }
        return try await operation()
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
        // The whole scan is one model session: memory warnings between the
        // per-crop inferences must never free the client mid-scan (the "9 items
        // analyzed, nothing recognized" field bug). Ends on every exit path,
        // including cancellation.
        lifecycle.beginSession()
        defer { endModelSession() }
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

            // --- Whole-image pass first: ask the model to list everything it
            // can see before we crop, catching items that fall between tiles.
            // Scoped in `do {}` so the full-resolution cgImage (~20-40MB) is
            // released before the crop loop allocates its own images. ---
            do {
                if let cgImage = await Task.detached(operation: { CropService.normalizedCGImage(from: image) }).value {
                    let fullOCR = await Task.detached { OCRService.recognizeText(in: cgImage) }.value
                    let wholeImageItems = try? await runInference {
                        try await self.llm.identifyAllItems(image: cgImage, ocrText: fullOCR)
                    }
                    if let items = wholeImageItems {
                        for item in items {
                            let dominated = item.name.lowercased() == "unknown"
                                || item.confidence < 0.15
                            guard !dominated else { continue }
                            results.append(item)
                        }
                        let reduceMotion = UIAccessibility.isReduceMotionEnabled
                        withMotion(reduceMotion, .spring(response: 0.35, dampingFraction: 0.7)) {
                            scannedItems = InventoryLogic.dedupe(results)
                        }
                    }
                }
            }

            if Task.isCancelled { return }
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
                do {
                    if cropResult.isTileFallback {
                        // Tiles are large regions that may contain multiple items;
                        // ask the model to list everything it sees in each tile.
                        let items = try await runInference {
                            try await self.llm.identifyAllItems(image: cropped, ocrText: ocrText)
                        }
                        for item in items {
                            let dominated = item.name.lowercased() == "unknown"
                                || item.confidence < 0.15
                            guard !dominated else { continue }
                            results.append(item)
                        }
                    } else {
                        let identified = try await runInference {
                            try await self.llm.identifyItem(image: cropped, ocrText: ocrText)
                        }
                        // Filter out empty-crop hallucinations: the model (and
                        // training data) uses "unknown" at ~0.1 confidence for
                        // background-only crops.
                        let dominated = identified.name.lowercased() == "unknown"
                            || identified.confidence < 0.15
                        guard !dominated else { continue }
                        results.append(identified)
                    }
                    // Gentle stream-in spring, but honour Reduce Motion.
                    let reduceMotion = UIAccessibility.isReduceMotionEnabled
                    withMotion(reduceMotion, .spring(response: 0.35, dampingFraction: 0.7)) {
                        scannedItems = InventoryLogic.dedupe(results)
                    }
                } catch {
                    print("[Scan] Crop \(index+1)/\(total) failed: \(error)")
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
        // One session for the whole generation, same rules as a scan.
        lifecycle.beginSession()
        defer { endModelSession() }
        await loadModelIfNeeded()

        let prompt = Self.recipePrompt(
            itemNames: names,
            prefs: RecipePreferences.current(defaults),
            mood: moodText,
            tasteProfile: TasteProfile.load()
        )
        let raw: String
        if defaults.bool(forKey: PrefKey.debugSimulateFailure) {
            // Hidden debug switch: return a deliberately broken reply so the
            // real error-and-retry UI can be demonstrated end to end.
            raw = "Sure! Here are some ideas: { this response is not valid json at all"
        } else {
            raw = (try? await runInference { try await self.llm.generateText(prompt: prompt) }) ?? ""
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
    nonisolated static func recipePrompt(
        itemNames: [String],
        prefs: RecipePreferences,
        mood: String = "",
        tasteProfile: TasteProfile? = nil
    ) -> String {
        let inventory = itemNames.joined(separator: ", ")

        var lines: [String] = []
        lines.append("You are a helpful home cooking assistant.")
        lines.append("Inventory: \(inventory).")
        let trimmedMood = mood.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedMood.isEmpty {
            lines.append("The user says: \"\(trimmedMood)\". Tailor your recipe suggestions to match this mood or craving.")
        }
        if let taste = tasteProfile, taste.hasContent {
            lines.append(taste.promptFragment())
        }
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
            "Suggest real, well-known dishes only, with their genuine standard ingredients. "
                + "Never invent a dish or force a dish to use only the inventory. "
                + "Assume salt, pepper, water and cooking oil are always on hand."
        )
        lines.append(
            "Respond ONLY with JSON of the form "
                + "{\"makeNow\":[{\"name\":\"\",\"ingredients\":[{\"name\":\"\",\"hasIt\":true}],"
                + "\"steps\":[\"\"],\"timeMinutes\":0,\"missingItems\":[]}],\"almostThere\":[...]}. "
                + "For every ingredient set hasIt true only if it is in the inventory. "
                + "makeNow is for dishes whose ingredients are all in the inventory. "
                + "almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems."
        )
        return lines.joined(separator: "\n")
    }
}
