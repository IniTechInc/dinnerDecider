package com.philwoolley.dinnerdecider

import android.app.Application
import android.content.ComponentCallbacks2
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow

/**
 * Application entry point. **Pure plumbing: no domain types, deliberately.**
 *
 * This class exists to turn the two platform memory callbacks into streams that anything
 * can observe, and nothing else. It emits the raw `Int` level straight from
 * [onTrimMemory] rather than a mapped contract enum, so it carries zero dependency on the
 * pure contract layer and never needs to be edited again as the port grows. The mapping
 * from a trim level to a memory-pressure level — including the four-producer funnel that
 * `onTrimMemory` alone cannot provide — belongs to the memory-pressure adapter
 * (plan Step 44), which is where it is unit-testable as a pure function.
 *
 * Why this is not enough on its own: every `TRIM_MEMORY_*` constant is deprecated as of
 * API 34, and foreground `TRIM_MEMORY_RUNNING_*` delivery has been unreliable since
 * Android 10. A port that wires only these two callbacks passes every unit test and then
 * behaves as though memory pressure never happens. The polled `ActivityManager.MemoryInfo`
 * probe in Step 44 is the producer that is actually guaranteed to fire.
 */
class DinnerDeciderApplication : Application(), ComponentCallbacks2 {

    private val _trimEvents = MutableSharedFlow<Int>(
        replay = 0,
        extraBufferCapacity = EVENT_BUFFER,
        onBufferOverflow = BufferOverflow.DROP_OLDEST,
    )

    /**
     * Raw levels as delivered to [onTrimMemory], e.g.
     * [ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL]. Never mapped here.
     */
    val trimEvents: SharedFlow<Int> = _trimEvents.asSharedFlow()

    private val _lowMemoryEvents = MutableSharedFlow<Unit>(
        replay = 0,
        extraBufferCapacity = EVENT_BUFFER,
        onBufferOverflow = BufferOverflow.DROP_OLDEST,
    )

    /** One emission per `onLowMemory()`. The consumer treats it as CRITICAL. */
    val lowMemoryEvents: SharedFlow<Unit> = _lowMemoryEvents.asSharedFlow()

    /**
     * Silences both streams. Set by the headless self-test (plan Step 39) via its launch
     * extra: the self-test measures peak footprint, and a pressure handler unloading the
     * model mid-measurement would destroy the number it exists to produce. Nothing else
     * may set this.
     */
    @Volatile
    var suppressMemoryCallbacks: Boolean = false

    @Deprecated("ComponentCallbacks2 levels are deprecated as of API 34; see class KDoc.")
    override fun onTrimMemory(level: Int) {
        @Suppress("DEPRECATION")
        super.onTrimMemory(level)
        if (suppressMemoryCallbacks) return
        _trimEvents.tryEmit(level)
    }

    @Deprecated("Deprecated in API 35 in favour of onTrimMemory; see class KDoc.")
    override fun onLowMemory() {
        @Suppress("DEPRECATION")
        super.onLowMemory()
        if (suppressMemoryCallbacks) return
        _lowMemoryEvents.tryEmit(Unit)
    }

    private companion object {
        /**
         * Pressure events arrive in bursts and are idempotent downstream, so a small
         * buffer that drops the oldest is correct: the newest level is the true one.
         */
        const val EVENT_BUFFER = 16
    }
}
