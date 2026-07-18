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

    // MARK: - Scan/recipe sessions (field bug: model freed between crops)

    /// Repro for the "9 items analyzed, nothing recognized" TestFlight report:
    /// after the 3.5GB load the system reliably posts a memory warning, and the
    /// moments BETWEEN per-crop inferences are `.ready`, so the old rules freed
    /// the model mid-scan and every remaining identify failed silently. During
    /// an active session a warning at `.ready` must be ignored.
    func testWarningBetweenInferencesDuringSessionDoesNotUnload() {
        var lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)
        XCTAssertTrue(lifecycle.beginInference())   // crop 1
        lifecycle.finishInference()                 // between crops: .ready

        let decision = lifecycle.handleMemoryPressure(.warning)

        XCTAssertEqual(decision, .nothingToDo)
        XCTAssertEqual(lifecycle.state, .ready)
        XCTAssertFalse(lifecycle.pendingUnload)
    }

    func testCriticalBetweenInferencesDuringSessionDefersToSessionEnd() {
        var lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        XCTAssertEqual(lifecycle.handleMemoryPressure(.critical), .deferred)
        XCTAssertEqual(lifecycle.state, .ready)
        XCTAssertTrue(lifecycle.pendingUnload)

        // Mid-session drain (called after every inference) must NOT free it.
        XCTAssertEqual(lifecycle.drainPendingUnload(), .nothingToDo)
        XCTAssertEqual(lifecycle.state, .ready)

        // Session over: the deferred critical unload finally runs.
        XCTAssertEqual(lifecycle.endSession(), .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)
    }

    func testWarningAtReadyOutsideSessionStillUnloads() {
        var lifecycle = ModelLifecycle()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        // Idle with no scan/recipe running: freeing on a warning is correct.
        XCTAssertEqual(lifecycle.handleMemoryPressure(.warning), .proceed)
        XCTAssertEqual(lifecycle.state, .unloading)
    }

    func testNestedSessionsOnlyEndWhenAllEnd() {
        var lifecycle = ModelLifecycle()
        lifecycle.beginSession()    // scan
        lifecycle.beginSession()    // recipes overlap
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        XCTAssertEqual(lifecycle.endSession(), .nothingToDo)
        // One session still active: warnings stay ignored.
        XCTAssertEqual(lifecycle.handleMemoryPressure(.warning), .nothingToDo)
        XCTAssertEqual(lifecycle.state, .ready)

        _ = lifecycle.endSession()
        XCTAssertEqual(lifecycle.handleMemoryPressure(.warning), .proceed)
    }

    func testEndSessionWithoutPendingUnloadKeepsModelResident() {
        var lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        _ = lifecycle.beginLoad()
        lifecycle.finishLoad(success: true)

        // No pressure during the scan: model stays warm for the next scan.
        XCTAssertEqual(lifecycle.endSession(), .nothingToDo)
        XCTAssertEqual(lifecycle.state, .ready)
    }
}
