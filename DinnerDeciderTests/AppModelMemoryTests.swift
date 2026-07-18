import XCTest
@testable import DinnerDecider

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
