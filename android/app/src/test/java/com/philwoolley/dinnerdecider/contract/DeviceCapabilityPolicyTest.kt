package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Test

private const val GIB = 1024L * 1024 * 1024

class DeviceCapabilityPolicyTest {

    /** A capable device: arm64, libs loaded, files present, 8 GB class, idle. */
    private fun capable(
        isLowRamDevice: Boolean = false,
        totalMemBytes: Long = 7_800L * 1024 * 1024,
        availMemBytes: Long = 4 * GIB,
        supportedAbis: List<String> = listOf("arm64-v8a"),
        freeDiskBytes: Long = 20 * GIB,
        nativeLibsPresent: Boolean = true,
        modelFilesPresent: Boolean = true,
        userForcedReal: Boolean = false,
    ) = DeviceSnapshot(
        isLowRamDevice = isLowRamDevice,
        totalMemBytes = totalMemBytes,
        availMemBytes = availMemBytes,
        supportedAbis = supportedAbis,
        freeDiskBytes = freeDiskBytes,
        nativeLibsPresent = nativeLibsPresent,
        modelFilesPresent = modelFilesPresent,
        userForcedReal = userForcedReal,
    )

    private fun mock(reason: MockReason) = RuntimeMode.Mock(reason)

    // MARK: - The happy path

    @Test
    fun `a capable device gets the real runtime`() {
        assertEquals(RuntimeMode.Real, DeviceCapabilityPolicy.evaluate(capable()))
    }

    // MARK: - Facts about what can execute

    /** The designed-in answer for an x86_64 emulator: degraded, never crashed. */
    @Test
    fun `an x86_64 emulator routes to mock with UNSUPPORTED_ABI`() {
        assertEquals(
            mock(MockReason.UNSUPPORTED_ABI),
            DeviceCapabilityPolicy.evaluate(capable(supportedAbis = listOf("x86_64", "x86"))),
        )
    }

    @Test
    fun `an armeabi-only device routes to mock with UNSUPPORTED_ABI`() {
        assertEquals(
            mock(MockReason.UNSUPPORTED_ABI),
            DeviceCapabilityPolicy.evaluate(capable(supportedAbis = listOf("armeabi-v7a"))),
        )
    }

    @Test
    fun `missing native libs routes to mock, never an UnsatisfiedLinkError`() {
        assertEquals(
            mock(MockReason.NATIVE_LIBS_MISSING),
            DeviceCapabilityPolicy.evaluate(capable(nativeLibsPresent = false)),
        )
    }

    // MARK: - Facts about what is installed

    @Test
    fun `absent model files route to mock with NO_MODEL_FILES`() {
        assertEquals(
            mock(MockReason.NO_MODEL_FILES),
            DeviceCapabilityPolicy.evaluate(capable(modelFilesPresent = false)),
        )
    }

    // MARK: - Memory

    @Test
    fun `the low-ram flag is a hard mock`() {
        assertEquals(
            mock(MockReason.LOW_RAM_DEVICE),
            DeviceCapabilityPolicy.evaluate(capable(isLowRamDevice = true)),
        )
    }

    /**
     * THE CASE THAT MATTERS, and it is real hardware, not a hypothetical: the
     * Galaxy XCover6 Pro on hand ships in a single 6 GB configuration worldwide and
     * lands near 5.4-5.7 GiB of `totalMem` after carveouts.
     *
     * `isLowRamDevice` is **false** on it — OEMs only set `ro.config.low_ram` for
     * the Android Go class — so the flag alone would wave this device straight
     * through to a 3.5 GB model load and an LMKD kill. Only the 7.0 GiB floor
     * catches it.
     */
    @Test
    fun `a 6 GB device with isLowRamDevice false still routes to mock`() {
        val sixGigPhone = capable(
            isLowRamDevice = false,
            totalMemBytes = (5.6 * GIB).toLong(),
            availMemBytes = 3 * GIB,
        )
        assertEquals(mock(MockReason.LOW_RAM_DEVICE), DeviceCapabilityPolicy.evaluate(sixGigPhone))
    }

    @Test
    fun `an 8 GB phone reporting 7_4 GiB clears the floor`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(capable(totalMemBytes = (7.4 * GIB).toLong())),
        )
    }

    @Test
    fun `the total-memory floor is inclusive at exactly 7 GiB`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(
                capable(totalMemBytes = DeviceCapabilityPolicy.MIN_TOTAL_MEM_BYTES),
            ),
        )
        assertEquals(
            mock(MockReason.LOW_RAM_DEVICE),
            DeviceCapabilityPolicy.evaluate(
                capable(totalMemBytes = DeviceCapabilityPolicy.MIN_TOTAL_MEM_BYTES - 1),
            ),
        )
    }

    @Test
    fun `too little free memory at load time is INSUFFICIENT_MEMORY`() {
        assertEquals(
            mock(MockReason.INSUFFICIENT_MEMORY),
            DeviceCapabilityPolicy.evaluate(capable(availMemBytes = 1 * GIB)),
        )
    }

    @Test
    fun `the available-memory floor is inclusive at exactly 2 GiB`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(
                capable(availMemBytes = DeviceCapabilityPolicy.MIN_AVAIL_MEM_BYTES),
            ),
        )
        assertEquals(
            mock(MockReason.INSUFFICIENT_MEMORY),
            DeviceCapabilityPolicy.evaluate(
                capable(availMemBytes = DeviceCapabilityPolicy.MIN_AVAIL_MEM_BYTES - 1),
            ),
        )
    }

    // MARK: - The user override

    @Test
    fun `userForcedReal overrides the low-ram flag`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(capable(isLowRamDevice = true, userForcedReal = true)),
        )
    }

    @Test
    fun `userForcedReal overrides the total-memory floor`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(
                capable(totalMemBytes = (5.6 * GIB).toLong(), userForcedReal = true),
            ),
        )
    }

    @Test
    fun `userForcedReal overrides INSUFFICIENT_MEMORY`() {
        assertEquals(
            RuntimeMode.Real,
            DeviceCapabilityPolicy.evaluate(capable(availMemBytes = 1 * GIB, userForcedReal = true)),
        )
    }

    /** Honouring the override here would turn a demo badge into a crash. */
    @Test
    fun `userForcedReal never overrides UNSUPPORTED_ABI`() {
        assertEquals(
            mock(MockReason.UNSUPPORTED_ABI),
            DeviceCapabilityPolicy.evaluate(
                capable(supportedAbis = listOf("x86_64"), userForcedReal = true),
            ),
        )
    }

    @Test
    fun `userForcedReal never overrides NATIVE_LIBS_MISSING`() {
        assertEquals(
            mock(MockReason.NATIVE_LIBS_MISSING),
            DeviceCapabilityPolicy.evaluate(capable(nativeLibsPresent = false, userForcedReal = true)),
        )
    }

    /** The override cannot conjure 4 GB of weights out of nothing. */
    @Test
    fun `userForcedReal never overrides NO_MODEL_FILES`() {
        assertEquals(
            mock(MockReason.NO_MODEL_FILES),
            DeviceCapabilityPolicy.evaluate(capable(modelFilesPresent = false, userForcedReal = true)),
        )
    }

    // MARK: - Reason set and unused inputs

    @Test
    fun `the reason set is exactly the six contract values`() {
        assertEquals(
            listOf(
                "NO_MODEL_FILES",
                "UNSUPPORTED_ABI",
                "NATIVE_LIBS_MISSING",
                "LOW_RAM_DEVICE",
                "INSUFFICIENT_MEMORY",
                "USER_FORCED",
            ),
            MockReason.entries.map { it.name },
        )
    }

    /**
     * Free disk is carried in the snapshot for the downloader, not for this
     * decision: by the time the files resolved they are already on disk, and there
     * is no disk-shaped value in [MockReason].
     */
    @Test
    fun `free disk does not affect the runtime mode`() {
        assertEquals(RuntimeMode.Real, DeviceCapabilityPolicy.evaluate(capable(freeDiskBytes = 0)))
    }

    /** Hardest fact first: an emulator with nothing installed still says ABI. */
    @Test
    fun `the ABI verdict outranks every other failure`() {
        assertEquals(
            mock(MockReason.UNSUPPORTED_ABI),
            DeviceCapabilityPolicy.evaluate(
                capable(
                    isLowRamDevice = true,
                    totalMemBytes = 2 * GIB,
                    availMemBytes = 0,
                    supportedAbis = listOf("x86_64"),
                    nativeLibsPresent = false,
                    modelFilesPresent = false,
                ),
            ),
        )
    }
}
