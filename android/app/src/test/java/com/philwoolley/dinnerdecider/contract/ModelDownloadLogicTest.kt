package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Port of `DinnerDeciderTests/ModelDownloadServiceTests.swift`, Swift names
 * preserved.
 *
 * Unit tests for the pure, side-effect-free download logic: the disk-space gate,
 * the size-verification decision, the metered-confirmation gate, and the state
 * machine. The transfer itself is not exercised here.
 */
class ModelDownloadLogicTest {

    // MARK: Disk-space gate

    @Test
    fun testDiskSpaceEnoughWithHeadroom() {
        assertTrue(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes = 5_000_000_000L))
    }

    @Test
    fun testDiskSpaceExactBoundaryPasses() {
        assertTrue(
            ModelDownloadLogic.hasEnoughDiskSpace(
                freeBytes = ModelDownloadLogic.REQUIRED_FREE_BYTES,
            ),
        )
    }

    @Test
    fun testDiskSpaceJustBelowBoundaryFails() {
        assertFalse(
            ModelDownloadLogic.hasEnoughDiskSpace(
                freeBytes = ModelDownloadLogic.REQUIRED_FREE_BYTES - 1,
            ),
        )
    }

    @Test
    fun testDiskSpaceInsufficient() {
        assertFalse(ModelDownloadLogic.hasEnoughDiskSpace(freeBytes = 1_000_000_000L))
    }

    // MARK: Size-verification decision

    @Test
    fun testSizeValidWhenExactMatch() {
        assertTrue(ModelDownloadLogic.sizeIsValid(actual = 3_460_000_000L, expected = 3_460_000_000L))
    }

    @Test
    fun testSizeInvalidWhenShort() {
        assertFalse(ModelDownloadLogic.sizeIsValid(actual = 3_000_000_000L, expected = 3_460_000_000L))
    }

    @Test
    fun testSizeInvalidWhenLarger() {
        assertFalse(ModelDownloadLogic.sizeIsValid(actual = 3_460_000_001L, expected = 3_460_000_000L))
    }

    /** Never accept a file you cannot vouch for. */
    @Test
    fun testSizeInvalidWhenExpectedUnknown() {
        assertFalse(ModelDownloadLogic.sizeIsValid(actual = 100, expected = 0))
    }

    // MARK: Metered-confirmation gate

    @Test
    fun testMayStartOnWifi() {
        assertTrue(
            ModelDownloadLogic.mayStartDownload(isMetered = false, userConfirmedMetered = false),
        )
    }

    @Test
    fun testBlockedOnCellularWithoutConfirmation() {
        assertFalse(
            ModelDownloadLogic.mayStartDownload(isMetered = true, userConfirmedMetered = false),
        )
    }

    @Test
    fun testAllowedOnCellularWithConfirmation() {
        assertTrue(
            ModelDownloadLogic.mayStartDownload(isMetered = true, userConfirmedMetered = true),
        )
    }

    // MARK: Overall fraction

    @Test
    fun testOverallFractionHalf() {
        assertEquals(0.5, ModelDownloadLogic.overallFraction(received = 50, total = 100), 0.0001)
    }

    @Test
    fun testOverallFractionZeroTotalIsZero() {
        assertEquals(0.0, ModelDownloadLogic.overallFraction(received = 50, total = 0), 0.0001)
    }

    @Test
    fun testOverallFractionCapsAtOne() {
        assertEquals(1.0, ModelDownloadLogic.overallFraction(received = 150, total = 100), 0.0001)
    }

    // MARK: State machine

    @Test
    fun testHappyPathTransitions() {
        var state: ModelDownloadState = ModelDownloadState.NotStarted
        state = ModelDownloadLogic.next(state, ModelDownloadLogic.Event.Start)
        assertEquals(ModelDownloadState.Downloading, state)
        state = ModelDownloadLogic.next(state, ModelDownloadLogic.Event.AllDownloaded)
        assertEquals(ModelDownloadState.Verifying, state)
        state = ModelDownloadLogic.next(state, ModelDownloadLogic.Event.VerifiedOK)
        assertEquals(ModelDownloadState.Done, state)
    }

    @Test
    fun testPauseResume() {
        assertEquals(
            ModelDownloadState.Paused,
            ModelDownloadLogic.next(ModelDownloadState.Downloading, ModelDownloadLogic.Event.Pause),
        )
        assertEquals(
            ModelDownloadState.Downloading,
            ModelDownloadLogic.next(ModelDownloadState.Paused, ModelDownloadLogic.Event.Resume),
        )
        assertEquals(
            ModelDownloadState.Downloading,
            ModelDownloadLogic.next(ModelDownloadState.Paused, ModelDownloadLogic.Event.Start),
        )
    }

    @Test
    fun testVerificationFailureGoesToFailed() {
        assertEquals(
            ModelDownloadState.Failed("bad size"),
            ModelDownloadLogic.next(
                ModelDownloadState.Verifying,
                ModelDownloadLogic.Event.VerificationFailed("bad size"),
            ),
        )
    }

    @Test
    fun testFailedFromAnyStateOnError() {
        assertEquals(
            ModelDownloadState.Failed("no network"),
            ModelDownloadLogic.next(
                ModelDownloadState.Downloading,
                ModelDownloadLogic.Event.Failed("no network"),
            ),
        )
        assertEquals(
            ModelDownloadState.Failed("io"),
            ModelDownloadLogic.next(
                ModelDownloadState.Verifying,
                ModelDownloadLogic.Event.Failed("io"),
            ),
        )
        assertEquals(
            ModelDownloadState.Failed("x"),
            ModelDownloadLogic.next(
                ModelDownloadState.NotStarted,
                ModelDownloadLogic.Event.Failed("x"),
            ),
        )
    }

    @Test
    fun testRetryFromFailedRestarts() {
        assertEquals(
            ModelDownloadState.Downloading,
            ModelDownloadLogic.next(
                ModelDownloadState.Failed("x"),
                ModelDownloadLogic.Event.Retry,
            ),
        )
        assertEquals(
            ModelDownloadState.Downloading,
            ModelDownloadLogic.next(
                ModelDownloadState.Failed("x"),
                ModelDownloadLogic.Event.Start,
            ),
        )
    }

    /** A stray callback must not be able to corrupt the UI. */
    @Test
    fun testUnhandledEventsAreNoOps() {
        assertEquals(
            ModelDownloadState.NotStarted,
            ModelDownloadLogic.next(ModelDownloadState.NotStarted, ModelDownloadLogic.Event.Pause),
        )
        assertEquals(
            ModelDownloadState.Done,
            ModelDownloadLogic.next(ModelDownloadState.Done, ModelDownloadLogic.Event.Pause),
        )
        assertEquals(
            ModelDownloadState.Downloading,
            ModelDownloadLogic.next(ModelDownloadState.Downloading, ModelDownloadLogic.Event.Resume),
        )
        assertEquals(
            ModelDownloadState.Verifying,
            ModelDownloadLogic.next(ModelDownloadState.Verifying, ModelDownloadLogic.Event.Pause),
        )
    }

    // MARK: File definitions match the selector and the required URLs

    @Test
    fun testFileNamesMatchLocator() {
        assertEquals(
            ModelFileSelection.PREFERRED_MMPROJ_NAME,
            ModelDownloadFile.MMPROJ.fileName,
        )
        assertEquals(
            ModelFileSelection.DEFAULT_MODEL_PREFERENCE[0],
            ModelDownloadFile.WEIGHTS.fileName,
        )
    }

    @Test
    fun testProjectorDownloadsFirst() {
        assertEquals(ModelDownloadFile.MMPROJ, ModelDownloadFile.entries.first())
    }

    @Test
    fun testRemoteURLsAreTheExpectedPublicURLs() {
        assertEquals(
            "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/" +
                "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
            ModelDownloadFile.WEIGHTS.remoteUrl,
        )
        assertEquals(
            "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/" +
                "mmproj-gemma-4-E4B-it-Q8_0.gguf",
            ModelDownloadFile.MMPROJ.remoteUrl,
        )
    }

    // MARK: FileDownloadProgress fraction

    @Test
    fun testFileProgressFraction() {
        val progress = FileDownloadProgress(bytesReceived = 25, totalBytes = 100)
        assertEquals(0.25, progress.fraction, 0.0001)
    }

    @Test
    fun testFileProgressFractionZeroTotal() {
        val progress = FileDownloadProgress(bytesReceived = 25, totalBytes = 0)
        assertEquals(0.0, progress.fraction, 0.0001)
    }
}

/**
 * The integrity constants, which iOS did not carry. Kept in a separate class so
 * [ModelDownloadLogicTest] stays at the twenty-five cases
 * `ModelDownloadServiceTests.swift` carries.
 */
class ModelDownloadIntegrityTest {

    /** Exact bytes, measured in the working tree. See `shared/runtime/model-files.md`. */
    @Test
    fun exactSizesMatchTheStagedFiles() {
        assertEquals(3_717_115_008L, ModelDownloadFile.WEIGHTS.exactBytes)
        assertEquals(559_874_816L, ModelDownloadFile.MMPROJ.exactBytes)
    }

    @Test
    fun digestsMatchTheStagedFiles() {
        assertEquals(
            "dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7",
            ModelDownloadFile.WEIGHTS.sha256,
        )
        assertEquals(
            "197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1",
            ModelDownloadFile.MMPROJ.sha256,
        )
        ModelDownloadFile.entries.forEach { file ->
            assertEquals(file.name, 64, file.sha256.length)
            assertTrue(file.name, file.sha256.all { it in "0123456789abcdef" })
        }
    }

    /**
     * `approxBytes` is a progress estimate and is deliberately wrong; it must never
     * be mistaken for the verification constant.
     */
    @Test
    fun approxBytesIsNotTheVerificationConstant() {
        ModelDownloadFile.entries.forEach { file ->
            assertTrue(file.name, file.approxBytes != file.exactBytes)
        }
    }

    /** 4.5 GB: ~4.0 GB of payload plus headroom for the atomic move. */
    @Test
    fun requiredFreeBytesCoversBothFilesWithHeadroom() {
        val payload = ModelDownloadFile.entries.sumOf { it.exactBytes }
        assertEquals(4_500_000_000L, ModelDownloadLogic.REQUIRED_FREE_BYTES)
        assertTrue(ModelDownloadLogic.REQUIRED_FREE_BYTES > payload)
    }

    @Test
    fun theExactSizesPassTheSizeGate() {
        ModelDownloadFile.entries.forEach { file ->
            assertTrue(file.name, ModelDownloadLogic.sizeIsValid(file.exactBytes, file.exactBytes))
        }
    }
}
