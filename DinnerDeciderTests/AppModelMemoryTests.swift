import CoreGraphics
import XCTest
@testable import DinnerDecider

/// A mock whose inference blocks until the test releases it, so a memory-pressure
/// event can be injected precisely while an inference is in flight. Plain class
/// (like `MockLLMService`) driven cooperatively via `Task.yield()`.
private final class GatedLLMService: LLMService {
    private(set) var isLoaded = false
    private(set) var unloadCount = 0
    private(set) var isInferring = false
    var release = false

    func loadModel() async throws { isLoaded = true }

    func unloadModel() {
        isLoaded = false
        unloadCount += 1
    }

    func identifyItem(image: CGImage, ocrText: String) async throws -> IdentifiedItem {
        await block()
        return IdentifiedItem(name: "x", brand: nil, category: "other", confidence: 1)
    }

    func generateText(prompt: String) async throws -> String {
        await block()
        return "{\"makeNow\":[],\"almostThere\":[]}"
    }

    private func block() async {
        isInferring = true
        while !release { await Task.yield() }
        isInferring = false
    }
}

/// Tests the memory-pressure mitigation: under pressure the resident model is
/// released so the system reclaims memory instead of jetsamming the app, and the
/// next scan transparently reloads it.
@MainActor
final class AppModelMemoryTests: XCTestCase {

    func testMemoryPressureUnloadsLoadedModel() async {
        let model = AppModel(llm: MockLLMService())
        await model.loadModelIfNeeded()
        XCTAssertTrue(model.isModelLoaded)

        model.handleMemoryPressure()

        XCTAssertFalse(model.isModelLoaded)
        XCTAssertFalse(model.llm.isLoaded)
        XCTAssertTrue(model.didReleaseModelForMemory)
    }

    func testMemoryPressureIsNoOpWhenModelNotLoaded() {
        let model = AppModel(llm: MockLLMService())
        XCTAssertFalse(model.isModelLoaded)

        model.handleMemoryPressure()

        XCTAssertFalse(model.didReleaseModelForMemory)
    }

    func testReloadAfterMemoryPressureClearsFlag() async {
        let model = AppModel(llm: MockLLMService())
        await model.loadModelIfNeeded()
        model.handleMemoryPressure()
        XCTAssertTrue(model.didReleaseModelForMemory)

        // Next scan path reloads the model.
        await model.loadModelIfNeeded()

        XCTAssertTrue(model.isModelLoaded)
        XCTAssertFalse(model.didReleaseModelForMemory)
    }

    // MARK: - Pressure during an in-flight inference (the crash this fixes)

    func testMemoryPressureDuringInferenceDoesNotUnloadModel() async {
        let gated = GatedLLMService()
        let model = AppModel(llm: gated)

        // Start a recipe generation; it will block inside generateText.
        let task = Task { await model.generateRecipes(fromItemNames: ["egg"]) }
        while !gated.isInferring { await Task.yield() }
        XCTAssertTrue(model.isModelLoaded)

        // Warning pressure mid-inference must NOT free the client.
        model.handleMemoryPressure(.warning)
        XCTAssertEqual(gated.unloadCount, 0, "must not unload while inference is in flight")
        XCTAssertTrue(model.isModelLoaded)

        // Let the inference finish and confirm the client survived intact.
        gated.release = true
        await task.value
        XCTAssertEqual(gated.unloadCount, 0)
        XCTAssertTrue(model.isModelLoaded)
    }

    func testCriticalPressureDuringInferenceDefersUnloadUntilDone() async {
        let gated = GatedLLMService()
        let model = AppModel(llm: gated)

        let task = Task { await model.generateRecipes(fromItemNames: ["egg"]) }
        while !gated.isInferring { await Task.yield() }

        // Critical pressure mid-inference is recorded but does not free now.
        model.handleMemoryPressure(.critical)
        XCTAssertEqual(gated.unloadCount, 0, "critical pressure must still not unload mid-inference")
        XCTAssertTrue(model.isModelLoaded)

        // Once the inference drains, the deferred unload runs exactly once.
        gated.release = true
        await task.value
        XCTAssertEqual(gated.unloadCount, 1, "deferred unload should run once inference completes")
        XCTAssertFalse(model.isModelLoaded)
        XCTAssertTrue(model.didReleaseModelForMemory)
    }

    // MARK: - modelLoadPhase

    func testModelLoadPhaseIsIdleWhenNotLoading() async {
        let model = AppModel(llm: MockLLMService())
        XCTAssertEqual(model.modelLoadPhase, .idle)

        await model.loadModelIfNeeded()
        // Load has completed, so we are idle again (not loading).
        XCTAssertEqual(model.modelLoadPhase, .idle)
    }

    func testModelLoadPhaseIsFirstLoadWhileLoading() {
        let model = AppModel(llm: MockLLMService())
        // Loading with no prior memory release is a first load.
        model.isLoadingModel = true
        XCTAssertEqual(model.modelLoadPhase, .firstLoad)
    }

    func testModelLoadPhaseIsReloadAfterMemoryPressure() async {
        let model = AppModel(llm: MockLLMService())
        await model.loadModelIfNeeded()
        model.handleMemoryPressure()
        XCTAssertTrue(model.didReleaseModelForMemory)

        // Loading again after a memory release reports the reload phase.
        model.isLoadingModel = true
        XCTAssertEqual(model.modelLoadPhase, .reloadingAfterMemory)
    }
}
