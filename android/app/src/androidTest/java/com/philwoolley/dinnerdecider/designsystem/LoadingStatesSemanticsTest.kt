package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.printToString
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.SemanticsProperties
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

/**
 * Accessibility assertions for the shared loading states.
 *
 * **These require a device or emulator** (`:app:connectedDebugAndroidTest`) and are therefore
 * NOT part of the host-JVM gate — `:app:assembleDebug`, `:app:lintDebug` and
 * `:app:testDebugUnitTest` neither run nor are blocked by them. They are written now so the
 * a11y contract this file establishes is executable the moment the arm64 emulator from plan
 * Step 1 exists, and so plan acceptance criterion 8 has something to run.
 *
 * What is being protected:
 * - The pot and the progress bar are **decorative**. iOS marks them `.accessibilityHidden`.
 *   If they leaked into the tree, TalkBack would stop on two meaningless nodes before
 *   reaching the only text that says what is happening.
 * - The rotating status copy is a **polite live region** (iOS `.updatesFrequently`). Without
 *   the property TalkBack never re-announces progress; with `Assertive` it would interrupt
 *   the user every two seconds for the length of a scan.
 */
class LoadingStatesSemanticsTest {

    @get:Rule
    val compose = createComposeRule()

    private fun isPoliteLiveRegion() = SemanticsMatcher.expectValue(
        SemanticsProperties.LiveRegion,
        LiveRegionMode.Polite,
    )

    @Test
    fun simmerPotMarkIsHiddenFromAccessibility() {
        compose.setContent {
            DinnerDeciderTheme {
                Box { SimmerPotMark() }
            }
        }
        // clearAndSetSemantics {} leaves nothing behind: no label, no role, no merged child.
        val tree = compose.onRoot(useUnmergedTree = true).printToString(maxDepth = 10)
        assertFalse(
            "SimmerPotMark is decorative and must not appear in the semantics tree:\n$tree",
            tree.contains("ContentDescription") || tree.contains("Text = "),
        )
    }

    @Test
    fun rotatingStatusIsAPoliteLiveRegionCarryingMessageAndExplainer() {
        compose.setContent {
            DinnerDeciderTheme {
                LoadingStateView(
                    messages = LoadingCopy.modelWarmup,
                    explainer = LoadingCopy.MODEL_EXPLAINER,
                )
            }
        }
        val expected = "${LoadingCopy.modelWarmup.first()}. ${LoadingCopy.MODEL_EXPLAINER}"
        compose.onNodeWithContentDescription(expected)
            .assertIsDisplayed()
            .assert(isPoliteLiveRegion())
    }

    @Test
    fun statusWithoutExplainerAnnouncesJustTheMessage() {
        compose.setContent {
            DinnerDeciderTheme {
                LoadingStateView(messages = listOf("Only line"), showsProgressBar = false)
            }
        }
        compose.onNodeWithContentDescription("Only line")
            .assertIsDisplayed()
            .assert(isPoliteLiveRegion())
    }

    @Test
    fun cancelButtonIsReachableAndInvokesItsCallback() {
        var cancelled = false
        compose.setContent {
            DinnerDeciderTheme {
                LoadingStateView(
                    messages = LoadingCopy.recipeThinking,
                    explainer = LoadingCopy.RECIPE_EXPLAINER,
                    onCancel = { cancelled = true },
                )
            }
        }
        compose.onNodeWithText("Cancel").performClick()
        assertTrue(cancelled)
    }

    @Test
    fun shimmeringItemRowExposesOnlyItsStatusLabel() {
        val label = "Looking at item 3 of 8..."
        compose.setContent {
            DinnerDeciderTheme {
                ShimmeringItemRow(statusLabel = label)
            }
        }
        compose.onNodeWithContentDescription(label)
            .assertIsDisplayed()
            .assert(isPoliteLiveRegion())

        // The three skeleton shapes inside it must not be separately focusable.
        val tree = compose.onRoot(useUnmergedTree = true).printToString(maxDepth = 10)
        assertTrue(
            "Expected exactly one ContentDescription on the row:\n$tree",
            tree.split("ContentDescription").size - 1 == 1,
        )
    }

    @Test
    fun shimmerThumbnailIsDecorative() {
        compose.setContent {
            DinnerDeciderTheme {
                Box { ShimmerThumbnail() }
            }
        }
        val tree = compose.onRoot(useUnmergedTree = true).printToString(maxDepth = 10)
        assertFalse(
            "ShimmerThumbnail is decorative and must not appear in the semantics tree:\n$tree",
            tree.contains("ContentDescription"),
        )
    }

    @Test
    fun reduceMotionCanBeForcedWithoutTouchingASystemSetting() {
        // LocalReduceMotion is the seam that makes the reduced-motion branch testable: the
        // alternative is flipping Settings.Global.ANIMATOR_DURATION_SCALE on the device, which
        // needs WRITE_SECURE_SETTINGS and leaks state between tests. Both branches must render
        // the same content - reduced motion removes movement, never information.
        compose.setContent {
            DinnerDeciderTheme {
                CompositionLocalProvider(LocalReduceMotion provides true) {
                    LoadingStateView(
                        messages = LoadingCopy.modelWarmup,
                        explainer = LoadingCopy.MODEL_EXPLAINER,
                    )
                }
            }
        }
        val expected = "${LoadingCopy.modelWarmup.first()}. ${LoadingCopy.MODEL_EXPLAINER}"
        compose.onNodeWithContentDescription(expected)
            .assertIsDisplayed()
            .assert(isPoliteLiveRegion())
    }

    @Test
    fun emptyMessageListDoesNotCrash() {
        compose.setContent {
            DinnerDeciderTheme {
                LoadingStateView(messages = emptyList(), showsProgressBar = true)
            }
        }
        compose.onRoot().assertIsDisplayed()
    }
}
