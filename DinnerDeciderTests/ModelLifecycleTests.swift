import XCTest
@testable import DinnerDecider

/// Unit tests for the pure model lifecycle state machine.
///
/// These encode the crash-fix guarantees directly: the client is never freed
/// while a load or inference is in flight, deferred unloads run once idle, and
/// duplicate loads coalesce. They are pure (no runtime, no async) so they are
/// fast and deterministic.
final class ModelLifecycleTests: XCTestCase {

    // MARK: - Pressure during load

    func testWarningPressureDuringLoadDoesNotUnload() {
        var lifecycle = ModelLifecycle()
        XCTAssertEqual(lifecycle.beginLoad(), .proceed)
        XCTAssertEqual(lifecycle.state, .loading)

        // A warning while loading is expected pressure: ignore it entirely.
        let decision = lifecycle.handleMemoryPressure(.warning)

        XCTAssertEqual(decision, .nothingToDo)
        XCTAssertEqual(lifecycle.state, .loading)
        XCTAssertFalse(lifecycle.pendingUnload)
    }

    func testCriticalPressureDuringLoadDefersUnloadNeverFrees() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()

        let decision = lifecycle.handleMemoryPressure(.critical)

        // Recorded, but the client is NOT freed mid-load.
        XCTAssertEqual(decision, .deferred)
        XCTAssertEqual(lifecycle.state, .loading)
        XCTAssertTrue(lifecycle.pendingUnload)
    }

    // MARK: - Pressure during inference

    func testWarningPressureDuringInferenceDoesNotUnload() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)
        XCTAssertTrue(lifecycle.beginInference())
        XCTAssertEqual(lifecycle.state, .inferring)

        let decision = lifecycle.handleMemoryPressure(.warning)

        XCTAssertEqual(decision, .nothingToDo)
        XCTAssertEqual(lifecycle.state, .inferring)
        XCTAssertFalse(lifecycle.pendingUnload)
    }

    // MARK: - Deferred unload drains once idle

    func testPendingUnloadExecutesAfterLoadCompletes() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        _ = lifecycle.requestUnload()           // deferred while loading
        XCTAssertTrue(lifecycle.pendingUnload)

        lifecycle.finishLoad(success: true)
        XCTAssertEqual(lifecycle.state, .ready)

        let decision = lifecycle.drainPendingUnload()

        XCTAssertEqual(decision, .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)
        XCTAssertFalse(lifecycle.pendingUnload)
    }

    func testPendingUnloadExecutesAfterInferenceCompletes() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)
        _ = lifecycle.beginInference()

        // Critical pressure mid-inference records the unload but does not free.
        XCTAssertEqual(lifecycle.handleMemoryPressure(.critical), .deferred)
        XCTAssertEqual(lifecycle.state, .inferring)

        lifecycle.finishInference()
        XCTAssertEqual(lifecycle.state, .ready)

        XCTAssertEqual(lifecycle.drainPendingUnload(), .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)
    }

    func testFailedLoadDropsDeferredUnload() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        _ = lifecycle.requestUnload()
        XCTAssertTrue(lifecycle.pendingUnload)

        lifecycle.finishLoad(success: false)

        XCTAssertEqual(lifecycle.state, .unloaded)
        XCTAssertFalse(lifecycle.pendingUnload)
        XCTAssertEqual(lifecycle.drainPendingUnload(), .nothingToDo)
    }

    // MARK: - Load coalescing

    func testDoubleLoadCoalesces() {
        var lifecycle = ModelLifecycle()
        XCTAssertEqual(lifecycle.beginLoad(), .proceed)

        // A second load while the first is in flight must not start another.
        XCTAssertEqual(lifecycle.beginLoad(), .coalesced)
        XCTAssertEqual(lifecycle.state, .loading)
    }

    func testLoadWhenAlreadyReadyIsNoOp() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        XCTAssertEqual(lifecycle.beginLoad(), .alreadyLoaded)
        XCTAssertEqual(lifecycle.state, .ready)
    }

    // MARK: - Unload deferral while inferring

    func testExplicitUnloadWhileInferringDefers() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)
        _ = lifecycle.beginInference()

        let decision = lifecycle.requestUnload()

        XCTAssertEqual(decision, .deferred)
        XCTAssertEqual(lifecycle.state, .inferring)   // still serving
        XCTAssertTrue(lifecycle.pendingUnload)
    }

    // MARK: - Idle behavior

    func testWarningPressureWhenReadyIdleUnloads() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        let decision = lifecycle.handleMemoryPressure(.warning)

        XCTAssertEqual(decision, .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)
    }

    func testPressureWhenUnloadedIsNoOp() {
        var lifecycle = ModelLifecycle()

        XCTAssertEqual(lifecycle.handleMemoryPressure(.warning), .nothingToDo)
        XCTAssertEqual(lifecycle.handleMemoryPressure(.critical), .nothingToDo)
        XCTAssertEqual(lifecycle.state, .unloaded)
    }

    func testUnloadCycleReturnsToUnloaded() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)
        XCTAssertEqual(lifecycle.requestUnload(), .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)

        lifecycle.finishUnload()

        XCTAssertEqual(lifecycle.state, .unloaded)
        XCTAssertFalse(lifecycle.pendingUnload)
    }
}
