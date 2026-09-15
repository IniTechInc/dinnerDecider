package com.philwoolley.dinnerdecider.contract

/**
 * Everything the runtime-mode decision is allowed to look at, captured as plain
 * values so the policy stays a pure function testable on the host JVM.
 *
 * The caller (plan Step 47's service container) fills this in from
 * `ActivityManager.MemoryInfo`, `Build.SUPPORTED_ABIS`, `StatFs` and the model
 * locator. Nothing in this file imports `android.*`, which is what lets the whole
 * policy be exercised in milliseconds with no emulator.
 */
data class DeviceSnapshot(
    /**
     * `ActivityManager.isLowRamDevice()`.
     *
     * **Near-useless as a gate — read [DeviceCapabilityPolicy] before relying on
     * it.** It is a straight read of the `ro.config.low_ram` system property, which
     * OEMs set only for Android Go configurations (the 2 GB-and-under class), so it
     * returns **false** on a 6 GB phone that cannot host this model at all.
     */
    val isLowRamDevice: Boolean,
    /** `ActivityManager.MemoryInfo.totalMem` — total physical RAM the kernel exposes. */
    val totalMemBytes: Long,
    /** `ActivityManager.MemoryInfo.availMem` at the moment of the load attempt. */
    val availMemBytes: Long,
    /** `Build.SUPPORTED_ABIS`. */
    val supportedAbis: List<String>,
    /**
     * Free bytes on the volume holding `filesDir`.
     *
     * Deliberately **not** consulted by [DeviceCapabilityPolicy.evaluate]: by the
     * time model files are resolved they are already on disk, and there is no
     * disk-shaped value in [MockReason]. It travels in the snapshot because the same
     * probe feeds `ModelDownloadLogic.hasEnoughDiskSpace`, which is where a
     * disk-space decision belongs.
     */
    val freeDiskBytes: Long,
    /** True once every `.so` in the load order resolved through `System.loadLibrary`. */
    val nativeLibsPresent: Boolean,
    /** True when both the weights and the vision projector resolved and verified. */
    val modelFilesPresent: Boolean,
    /**
     * The Settings "Try the real model anyway" override.
     *
     * Overrides the two memory verdicts and nothing else — see
     * [DeviceCapabilityPolicy.evaluate].
     */
    val userForcedReal: Boolean,
)

/** Why the app is running canned data instead of the on-device model. */
enum class MockReason {
    /** Weights and/or the vision projector are not on the device. */
    NO_MODEL_FILES,

    /**
     * `arm64-v8a` is absent from `Build.SUPPORTED_ABIS`. This is the designed-in
     * answer for an x86_64 emulator: degraded to mock mode with the demo badge,
     * never crashed.
     */
    UNSUPPORTED_ABI,

    /** One or more `.so` failed to load. Never an `UnsatisfiedLinkError` crash. */
    NATIVE_LIBS_MISSING,

    /**
     * The device does not have enough physical RAM to host the model. Covers both
     * the `isLowRamDevice` flag and — far more importantly — the
     * [DeviceCapabilityPolicy.MIN_TOTAL_MEM_BYTES] floor.
     */
    LOW_RAM_DEVICE,

    /** Enough RAM in total, but not enough free right now to load. */
    INSUFFICIENT_MEMORY,

    /**
     * Reserved for a mock selection attributable to an explicit user choice.
     *
     * [DeviceCapabilityPolicy.evaluate] never returns this today: [DeviceSnapshot]
     * carries only [DeviceSnapshot.userForcedReal], the *inverse* override, and
     * forcing real mode can only produce [RuntimeMode.Real] or a verdict the
     * override is not allowed to touch. The value is part of the contract's reason
     * set, so it is declared here rather than invented later at a call site.
     */
    USER_FORCED,
}

/** Which inference backend the app should run with. */
sealed interface RuntimeMode {
    /** The real on-device Gemma 4 runtime. */
    data object Real : RuntimeMode

    /** Canned data, surfaced in the UI as the demo badge plus [reason]. */
    data class Mock(val reason: MockReason) : RuntimeMode
}

/**
 * Decides whether this device gets the real on-device model or the mock.
 *
 * Pure function over a [DeviceSnapshot]; no I/O, no `android.*`, no caching.
 *
 * ### `isLowRamDevice()` is not the protection
 * The [DeviceSnapshot.isLowRamDevice] early-out is kept because it is free and
 * because a device that *does* set `ro.config.low_ram` is unambiguously out — but
 * it is not what keeps this app off under-specified hardware. OEMs set that
 * property only for Android Go configurations (roughly the 2 GB-and-under class).
 * A 6 GB phone reports `false` and would sail straight past it.
 *
 * **The [MIN_TOTAL_MEM_BYTES] floor is the only check doing real work here.**
 * If it were removed, the low-RAM flag would let the model load on hardware where
 * decode touches essentially every weight page per token, producing major-fault
 * thrash against UFS and an LMKD kill — the same class of failure as the iOS
 * jetsam, reached through a different killer.
 *
 * ### What the user override can and cannot do
 * [DeviceSnapshot.userForcedReal] overrides [MockReason.LOW_RAM_DEVICE] and
 * [MockReason.INSUFFICIENT_MEMORY] — the two judgement calls, where the user is
 * entitled to take the risk on their own device. It never overrides
 * [MockReason.UNSUPPORTED_ABI] or [MockReason.NATIVE_LIBS_MISSING], because those
 * are statements of fact about what can execute, and honouring the override there
 * would turn a demo badge into an `UnsatisfiedLinkError`. It equally cannot
 * override [MockReason.NO_MODEL_FILES]: the override cannot conjure 4 GB of
 * weights out of nothing.
 */
object DeviceCapabilityPolicy {

    /** The one ABI the llama.cpp payload is built for. */
    const val REQUIRED_ABI: String = "arm64-v8a"

    /**
     * Total-RAM floor: **7.0 GiB**.
     *
     * Not a comfort margin, a floor. A phone marketed as "8 GB" reports roughly
     * 7.4-7.8 GiB of `totalMem` after kernel and hardware carveouts, so 7.0 GiB is
     * the smallest threshold that still admits genuine 8 GB devices — and iOS was
     * jetsammed **on** an 8 GB phone, so 8 GB is the entry ticket, not headroom.
     * Anything reporting less than this is a 6 GB device or smaller (the confirmed
     * test hardware, a Galaxy XCover6 Pro, lands near 5.4-5.7 GiB) and cannot host
     * 3.5 GB of weights plus the projector without thrashing.
     */
    const val MIN_TOTAL_MEM_BYTES: Long = 7L * 1024 * 1024 * 1024

    /**
     * Free-RAM floor at load time: **2.0 GiB**. **PROVISIONAL.**
     *
     * This number is not yet measured on Android and must be re-tuned once plan
     * Step 39 reports real CPU-path PSS on a device that clears
     * [MIN_TOTAL_MEM_BYTES].
     *
     * The iOS figure (~4.8 GB) cannot simply be carried over: it was a **wired
     * Metal** number. On Android inference is CPU-only and the weights are
     * `MAP_SHARED|PROT_READ` on a regular file — clean, file-backed and evictable,
     * a fundamentally different memory profile. Pulling in the other direction,
     * `MAP_POPULATE` pre-faults the whole 3.5 GB at load and both LMKD and
     * `Debug.getMemoryInfo()` count clean file-backed pages in PSS, so the killer
     * sees more than just KV cache plus compute buffers. Those two effects have not
     * been weighed against each other on real hardware yet. 2.0 GiB is a deliberate
     * placeholder that blocks an obviously doomed load without pretending to be
     * derived from a measurement.
     */
    const val MIN_AVAIL_MEM_BYTES: Long = 2L * 1024 * 1024 * 1024

    /**
     * Evaluate a snapshot. Checks run hardest-fact-first so the reason the UI shows
     * is the most fundamental one: what can execute, then what is installed, then
     * what fits.
     */
    fun evaluate(snapshot: DeviceSnapshot): RuntimeMode {
        // 1. Facts about what can execute at all. Not overridable.
        if (REQUIRED_ABI !in snapshot.supportedAbis) {
            return RuntimeMode.Mock(MockReason.UNSUPPORTED_ABI)
        }
        if (!snapshot.nativeLibsPresent) {
            return RuntimeMode.Mock(MockReason.NATIVE_LIBS_MISSING)
        }

        // 2. Facts about what is installed. The override cannot conjure weights.
        if (!snapshot.modelFilesPresent) {
            return RuntimeMode.Mock(MockReason.NO_MODEL_FILES)
        }

        // 3. Judgement calls about what fits. The user may take this risk.
        if (snapshot.userForcedReal) {
            return RuntimeMode.Real
        }
        // Kept, but see the class KDoc: this flag is false on a 6 GB device.
        if (snapshot.isLowRamDevice) {
            return RuntimeMode.Mock(MockReason.LOW_RAM_DEVICE)
        }
        // The check that actually protects the app.
        if (snapshot.totalMemBytes < MIN_TOTAL_MEM_BYTES) {
            return RuntimeMode.Mock(MockReason.LOW_RAM_DEVICE)
        }
        if (snapshot.availMemBytes < MIN_AVAIL_MEM_BYTES) {
            return RuntimeMode.Mock(MockReason.INSUFFICIENT_MEMORY)
        }

        return RuntimeMode.Real
    }
}
