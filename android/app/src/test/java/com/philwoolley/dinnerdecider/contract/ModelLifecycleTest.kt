package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Unit tests for the pure model lifecycle state machine.
 *
 * Line-for-line port of `DinnerDeciderTests/ModelLifecycleTests.swift` - **all
 * 17 tests, Swift names preserved**, so a failure here maps 1:1 onto the Swift
 * test that pins the same rule. The corrected guarantee table in
 * `shared/pipeline/model-lifecycle.md` lists all 17 and is the checklist.
 *
 * These encode the crash-fix guarantees directly: the client is never freed
 * while a load or inference is in flight, deferred unloads run once idle,
 * duplicate loads coalesce, and a session protects the gaps between a scan's
 * per-crop inferences. They are pure (no runtime, no async, no `android.*`) so
 * they are fast and deterministic on the host JVM.
 *
 * Two of the 17 are easy to miss and were absent from the shared doc's table
 * until Sept 14 2026:
 * - [testWarningPressureWhenReadyIdleUnloads] - load-bearing. It pins that
 *   WARNING is ignored *only* while BUSY or inside a session; at READY and idle
 *   it really does unload. A port treating WARNING as always-ignorable keeps a
 *   multi-GB model resident through exactly the pressure it exists to relieve.
 * - [testUnloadCycleReturnsToUnloaded].
 */
class ModelLifecycleTest {

    // ------------------------------------------------------------------
    // Pressure during load
    // ------------------------------------------------------------------

    @Test
    fun testWarningPressureDuringLoadDoesNotUnload() {
        val lifecycle = ModelLifecycle()
        assertEquals(LoadDecision.PROCEED, lifecycle.beginLoad())
        assertEquals(ModelState.LOADING, lifecycle.state)

        // A warning while loading is expected pressure: ignore it entirely.
        val decision = lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING)

        assertEquals(UnloadDecision.NOTHING_TO_DO, decision)
        assertEquals(ModelState.LOADING, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
    }

    @Test
    fun testCriticalPressureDuringLoadDefersUnloadNeverFrees() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()

        val decision = lifecycle.handleMemoryPressure(MemoryPressureLevel.CRITICAL)

        // Recorded, but the client is NOT freed mid-load.
        assertEquals(UnloadDecision.DEFERRED, decision)
        assertEquals(ModelState.LOADING, lifecycle.state)
        assertTrue(lifecycle.pendingUnload)
    }

    // ------------------------------------------------------------------
    // Pressure during inference
    // ------------------------------------------------------------------

    @Test
    fun testWarningPressureDuringInferenceDoesNotUnload() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)
        assertTrue(lifecycle.beginInference())
        assertEquals(ModelState.INFERRING, lifecycle.state)

        val decision = lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING)

        assertEquals(UnloadDecision.NOTHING_TO_DO, decision)
        assertEquals(ModelState.INFERRING, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
    }

    // ------------------------------------------------------------------
    // Deferred unload drains once idle
    // ------------------------------------------------------------------

    @Test
    fun testPendingUnloadExecutesAfterLoadCompletes() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.requestUnload() // deferred while loading
        assertTrue(lifecycle.pendingUnload)

        lifecycle.finishLoad(success = true)
        assertEquals(ModelState.READY, lifecycle.state)

        val decision = lifecycle.drainPendingUnload()

        assertEquals(UnloadDecision.PROCEED, decision)
        assertEquals(ModelState.UNLOADING, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
    }

    @Test
    fun testPendingUnloadExecutesAfterInferenceCompletes() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)
        lifecycle.beginInference()

        // Critical pressure mid-inference records the unload but does not free.
        assertEquals(UnloadDecision.DEFERRED, lifecycle.handleMemoryPressure(MemoryPressureLevel.CRITICAL))
        assertEquals(ModelState.INFERRING, lifecycle.state)

        lifecycle.finishInference()
        assertEquals(ModelState.READY, lifecycle.state)

        assertEquals(UnloadDecision.PROCEED, lifecycle.drainPendingUnload())
        assertEquals(ModelState.UNLOADING, lifecycle.state)
    }

    @Test
    fun testFailedLoadDropsDeferredUnload() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.requestUnload()
        assertTrue(lifecycle.pendingUnload)

        lifecycle.finishLoad(success = false)

        assertEquals(ModelState.UNLOADED, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.drainPendingUnload())
    }

    // ------------------------------------------------------------------
    // Load coalescing
    // ------------------------------------------------------------------

    @Test
    fun testDoubleLoadCoalesces() {
        val lifecycle = ModelLifecycle()
        assertEquals(LoadDecision.PROCEED, lifecycle.beginLoad())

        // A second load while the first is in flight must not start another.
        assertEquals(LoadDecision.COALESCED, lifecycle.beginLoad())
        assertEquals(ModelState.LOADING, lifecycle.state)
    }

    @Test
    fun testLoadWhenAlreadyReadyIsNoOp() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        assertEquals(LoadDecision.ALREADY_LOADED, lifecycle.beginLoad())
        assertEquals(ModelState.READY, lifecycle.state)
    }

    // ------------------------------------------------------------------
    // Unload deferral while inferring
    // ------------------------------------------------------------------

    @Test
    fun testExplicitUnloadWhileInferringDefers() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)
        lifecycle.beginInference()

        val decision = lifecycle.requestUnload()

        assertEquals(UnloadDecision.DEFERRED, decision)
        assertEquals(ModelState.INFERRING, lifecycle.state) // still serving
        assertTrue(lifecycle.pendingUnload)
    }

    // ------------------------------------------------------------------
    // Idle behavior
    // ------------------------------------------------------------------

    @Test
    fun testWarningPressureWhenReadyIdleUnloads() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        val decision = lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING)

        assertEquals(UnloadDecision.PROCEED, decision)
        assertEquals(ModelState.UNLOADING, lifecycle.state)
    }

    @Test
    fun testPressureWhenUnloadedIsNoOp() {
        val lifecycle = ModelLifecycle()

        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING))
        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.handleMemoryPressure(MemoryPressureLevel.CRITICAL))
        assertEquals(ModelState.UNLOADED, lifecycle.state)
    }

    @Test
    fun testUnloadCycleReturnsToUnloaded() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)
        assertEquals(UnloadDecision.PROCEED, lifecycle.requestUnload())
        assertEquals(ModelState.UNLOADING, lifecycle.state)

        lifecycle.finishUnload()

        assertEquals(ModelState.UNLOADED, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
    }

    // ------------------------------------------------------------------
    // Scan/recipe sessions (field bug: model freed between crops)
    // ------------------------------------------------------------------

    /**
     * Repro for the "9 items analyzed, nothing recognized" TestFlight report:
     * after the 3.5GB load the system reliably posts a memory warning, and the
     * moments BETWEEN per-crop inferences are READY, so the old rules freed the
     * model mid-scan and every remaining identify failed silently. During an
     * active session a warning at READY must be ignored.
     */
    @Test
    fun testWarningBetweenInferencesDuringSessionDoesNotUnload() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)
        assertTrue(lifecycle.beginInference()) // crop 1
        lifecycle.finishInference() // between crops: READY

        val decision = lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING)

        assertEquals(UnloadDecision.NOTHING_TO_DO, decision)
        assertEquals(ModelState.READY, lifecycle.state)
        assertFalse(lifecycle.pendingUnload)
    }

    @Test
    fun testCriticalBetweenInferencesDuringSessionDefersToSessionEnd() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        assertEquals(UnloadDecision.DEFERRED, lifecycle.handleMemoryPressure(MemoryPressureLevel.CRITICAL))
        assertEquals(ModelState.READY, lifecycle.state)
        assertTrue(lifecycle.pendingUnload)

        // Mid-session drain (called after every inference) must NOT free it.
        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.drainPendingUnload())
        assertEquals(ModelState.READY, lifecycle.state)

        // Session over: the deferred critical unload finally runs.
        assertEquals(UnloadDecision.PROCEED, lifecycle.endSession())
        assertEquals(ModelState.UNLOADING, lifecycle.state)
    }

    @Test
    fun testWarningAtReadyOutsideSessionStillUnloads() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        // Idle with no scan/recipe running: freeing on a warning is correct.
        assertEquals(UnloadDecision.PROCEED, lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING))
        assertEquals(ModelState.UNLOADING, lifecycle.state)
    }

    @Test
    fun testNestedSessionsOnlyEndWhenAllEnd() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginSession() // scan
        lifecycle.beginSession() // recipes overlap
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.endSession())
        // One session still active: warnings stay ignored.
        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING))
        assertEquals(ModelState.READY, lifecycle.state)

        lifecycle.endSession()
        assertEquals(UnloadDecision.PROCEED, lifecycle.handleMemoryPressure(MemoryPressureLevel.WARNING))
    }

    @Test
    fun testEndSessionWithoutPendingUnloadKeepsModelResident() {
        val lifecycle = ModelLifecycle()
        lifecycle.beginSession()
        lifecycle.beginLoad()
        lifecycle.finishLoad(success = true)

        // No pressure during the scan: model stays warm for the next scan.
        assertEquals(UnloadDecision.NOTHING_TO_DO, lifecycle.endSession())
        assertEquals(ModelState.READY, lifecycle.state)
    }
}
