package com.philwoolley.dinnerdecider.designsystem

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Pins the loading copy to `LoadingCopy` in
 * `DinnerDecider/Views/Components/LoadingStates.swift`, string for string.
 *
 * These lines are the only thing on screen for the length of a scan, and on Android that is
 * longer than on iOS (inference is CPU-only — plan finding 0.1). They are also the app's tone
 * of voice at its most visible. A drifted word here is invisible in review and obvious to a
 * user comparing the two apps, so the strings are asserted literally rather than by shape.
 */
class LoadingCopyTest {

    @Test
    fun modelWarmupMatchesIos() {
        assertEquals(
            listOf(
                "Waking up the chef... (first scan takes about 20 seconds)",
                "Loading Gemma into memory...",
                "Almost ready...",
            ),
            LoadingCopy.modelWarmup,
        )
    }

    @Test
    fun modelReloadMatchesIos() {
        assertEquals(
            listOf(
                "Warming up again (freed memory to keep things stable)...",
                "Loading Gemma back into memory...",
                "Almost ready...",
            ),
            LoadingCopy.modelReload,
        )
    }

    @Test
    fun recipeThinkingMatchesIos() {
        assertEquals(
            listOf(
                "Peeking into your pantry...",
                "Tasting a few ideas...",
                "Pairing your ingredients...",
                "Simmering some suggestions...",
                "Plating up options...",
            ),
            LoadingCopy.recipeThinking,
        )
    }

    @Test
    fun explainersMatchIos() {
        assertEquals(
            "This all runs on your device, so it takes a moment the first time.",
            LoadingCopy.MODEL_EXPLAINER,
        )
        assertEquals(
            "Cooking up ideas from what you have. Everything stays on your device.",
            LoadingCopy.RECIPE_EXPLAINER,
        )
    }

    @Test
    fun warmupAndReloadAreDistinct() {
        // The reload copy exists to explain that memory was freed on purpose. Collapsing it
        // into the warm-up copy makes a deliberate recovery look like the app starting over.
        assertFalse(LoadingCopy.modelWarmup == LoadingCopy.modelReload)
        assertTrue(LoadingCopy.modelReload.first().contains("freed memory"))
    }

    @Test
    fun ellipsesAreThreePeriodsNotTheUnicodeCharacter() {
        // iOS uses "..." throughout. A U+2026 would read differently to a screen reader and
        // would not round-trip against the Swift source.
        val all = LoadingCopy.modelWarmup + LoadingCopy.modelReload + LoadingCopy.recipeThinking +
            listOf(LoadingCopy.MODEL_EXPLAINER, LoadingCopy.RECIPE_EXPLAINER)
        all.forEach { line ->
            assertFalse("\"$line\" contains U+2026; iOS uses three periods.", line.contains('…'))
            assertFalse("\"$line\" contains an em dash; iOS copy uses none.", line.contains('—'))
        }
    }

    @Test
    fun rotationIntervalMatchesTheIosTimer() {
        assertEquals(2_000L, LoadingCopy.ROTATION_INTERVAL_MS)
    }

    @Test
    fun everyRotationHasAtLeastTwoLinesSoTheScreenNeverLooksStuck() {
        listOf(LoadingCopy.modelWarmup, LoadingCopy.modelReload, LoadingCopy.recipeThinking)
            .forEach { assertTrue(it.size >= 2) }
    }
}
