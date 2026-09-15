package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Port of `DinnerDeciderTests/ModelFileLocatorTests.swift`, Swift names preserved. */
class ModelFileSelectionTest {

    private val candidates = listOf(
        "gemma-4-E4B-it-Q4_K_M.gguf",
        "gemma-4-E4B-it-finetuned-Q4_K_M.gguf",
        "mmproj-F16.gguf",
        "some-other-model.gguf",
        "README.txt",
    )

    @Test
    fun testPrefersSelectedFileWhenPresent() {
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = candidates,
            preferred = "gemma-4-E4B-it-finetuned-Q4_K_M.gguf",
        )
        assertEquals("gemma-4-E4B-it-finetuned-Q4_K_M.gguf", chosen)
    }

    @Test
    fun testFallsBackToFirstMatchWhenPreferredMissing() {
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = candidates,
            preferred = "gemma-4-E4B-it-does-not-exist.gguf",
        )
        assertEquals("gemma-4-E4B-it-Q4_K_M.gguf", chosen)
    }

    @Test
    fun testFallsBackWhenNoPreferenceSet() {
        val chosen = ModelFileSelection.chooseModelFile(candidates = candidates, preferred = null)
        assertEquals("gemma-4-E4B-it-Q4_K_M.gguf", chosen)
    }

    @Test
    fun testExcludesMmprojAndNonMatchingFiles() {
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf("mmproj-F16.gguf", "some-other-model.gguf", "README.txt"),
            preferred = null,
        )
        assertNull(chosen)
    }

    @Test
    fun testReturnsNilForEmptyCandidates() {
        assertNull(ModelFileSelection.chooseModelFile(emptyList(), preferred = "anything.gguf"))
    }

    // MARK: - Real model filename matching

    @Test
    fun testChoosesRealQ4WeightsFile() {
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf("gemma-4-E4B-it-Q4_0.gguf", "mmproj-gemma-4-E4B-it-Q8_0.gguf"),
            preferred = null,
        )
        assertEquals("gemma-4-E4B-it-Q4_0.gguf", chosen)
    }

    // MARK: - Memory-optimized default preference

    @Test
    fun testPrefersSmallerQuantOverQ4WhenBothPresent() {
        // No pinned preference: the memory-optimized Q3_K_S should win over the
        // larger Q4_0 to keep memory down on device.
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf(
                "gemma-4-E4B-it-Q4_0.gguf",
                "gemma-4-E4B-it-Q3_K_S.gguf",
                "mmproj-gemma-4-E4B-it-Q8_0.gguf",
            ),
            preferred = null,
        )
        assertEquals("gemma-4-E4B-it-Q3_K_S.gguf", chosen)
    }

    @Test
    fun testUserPinnedFileStillWinsOverDefaultPreference() {
        // An explicit user selection always overrides the built-in preference,
        // even when it selects the memory hog.
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf("gemma-4-E4B-it-Q4_0.gguf", "gemma-4-E4B-it-Q3_K_S.gguf"),
            preferred = "gemma-4-E4B-it-Q4_0.gguf",
        )
        assertEquals("gemma-4-E4B-it-Q4_0.gguf", chosen)
    }

    @Test
    fun testPrefersIQ3XXSOverQ3AndQ4WhenAllPresent() {
        // Full preference order: the most memory-efficient quant (UD-IQ3_XXS) wins
        // over Q3_K_S and Q4_0 when nothing is pinned.
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf(
                "gemma-4-E4B-it-Q4_0.gguf",
                "gemma-4-E4B-it-Q3_K_S.gguf",
                "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
            ),
            preferred = null,
        )
        assertEquals("gemma-4-E4B-it-UD-IQ3_XXS.gguf", chosen)
    }

    @Test
    fun testFallsBackToFirstEligibleWhenNoPreferredQuantPresent() {
        // Neither pinned nor a memory-optimized quant present: unchanged fallback
        // to the first eligible file.
        val chosen = ModelFileSelection.chooseModelFile(
            candidates = listOf("gemma-4-E4B-it-Q4_K_M.gguf", "gemma-4-E4B-it-Q4_0.gguf"),
            preferred = null,
        )
        assertEquals("gemma-4-E4B-it-Q4_K_M.gguf", chosen)
    }

    // MARK: - Projector selection

    @Test
    fun testPrefersQ8ProjectorWhenPresent() {
        val chosen = ModelFileSelection.chooseMmprojFile(
            listOf("mmproj-gemma-4-E4B-it-BF16.gguf", "mmproj-gemma-4-E4B-it-Q8_0.gguf"),
        )
        assertEquals("mmproj-gemma-4-E4B-it-Q8_0.gguf", chosen)
    }

    @Test
    fun testFallsBackToAnyProjectorWhenPreferredMissing() {
        val chosen = ModelFileSelection.chooseMmprojFile(
            listOf("mmproj-F16.gguf", "gemma-4-E4B-it-Q4_0.gguf"),
        )
        assertEquals("mmproj-F16.gguf", chosen)
    }

    @Test
    fun testReturnsNilWhenNoProjectorPresent() {
        assertNull(
            ModelFileSelection.chooseMmprojFile(listOf("gemma-4-E4B-it-Q4_0.gguf", "README.txt")),
        )
    }
}

/**
 * The case-sensitivity asymmetry, which the Swift suite leaves implicit. Kept in a
 * separate class so [ModelFileSelectionTest] stays at the thirteen cases
 * `ModelFileLocatorTests.swift` carries.
 */
class ModelFileSelectionCaseTest {

    /**
     * The weights prefix is **case-sensitive**: `E4B` is part of the model's real
     * name, so a lower-cased spelling is not the file this runtime expects.
     */
    @Test
    fun theWeightsPrefixIsCaseSensitive() {
        assertNull(
            ModelFileSelection.chooseModelFile(
                candidates = listOf("gemma-4-e4b-it-Q4_0.gguf", "GEMMA-4-E4B-IT-Q4_0.gguf"),
                preferred = null,
            ),
        )
        assertEquals(
            "gemma-4-E4B-it-Q4_0.gguf",
            ModelFileSelection.chooseModelFile(
                candidates = listOf("gemma-4-e4b-it-Q4_0.gguf", "gemma-4-E4B-it-Q4_0.gguf"),
                preferred = null,
            ),
        )
    }

    /** The `.gguf` suffix check is **case-insensitive**, unlike the prefix. */
    @Test
    fun theWeightsSuffixIsCaseInsensitive() {
        assertEquals(
            "gemma-4-E4B-it-Q4_0.GGUF",
            ModelFileSelection.chooseModelFile(
                candidates = listOf("gemma-4-E4B-it-Q4_0.GGUF"),
                preferred = null,
            ),
        )
    }

    /** The `mmproj` exclusion is **case-insensitive**, unlike the prefix. */
    @Test
    fun theMmprojExclusionIsCaseInsensitive() {
        assertNull(
            ModelFileSelection.chooseModelFile(
                candidates = listOf("gemma-4-E4B-it-MMPROJ-Q8_0.gguf"),
                preferred = null,
            ),
        )
    }

    /** Both projector checks are case-insensitive, unlike the weights prefix. */
    @Test
    fun bothProjectorChecksAreCaseInsensitive() {
        assertTrue(ModelFileSelection.isMmproj("MMPROJ-F16.GGUF"))
        assertTrue(ModelFileSelection.isMmproj("MmProj-gemma.gguf"))
        assertFalse(ModelFileSelection.isMmproj("projector-F16.gguf"))
        assertFalse(ModelFileSelection.isMmproj("mmproj-F16.bin"))
        assertEquals(
            "MMPROJ-F16.GGUF",
            ModelFileSelection.chooseMmprojFile(listOf("MMPROJ-F16.GGUF")),
        )
    }

    /** A fine-tune named per the convention is eligible with no code change. */
    @Test
    fun aFineTunedBuildIsEligibleByPrefix() {
        assertEquals(
            "gemma-4-E4B-it-finetuned-sept.gguf",
            ModelFileSelection.chooseModelFile(
                candidates = listOf("gemma-4-E4B-it-finetuned-sept.gguf"),
                preferred = null,
            ),
        )
    }

    @Test
    fun thePinKeyMatchesIos() {
        assertEquals("selectedModelFileName", ModelFileSelection.SELECTED_MODEL_KEY)
    }
}
