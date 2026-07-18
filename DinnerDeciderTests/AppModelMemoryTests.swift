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
}
