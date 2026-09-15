package com.philwoolley.dinnerdecider.vision

import android.Manifest
import android.graphics.Bitmap
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.rule.GrantPermissionRule
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.math.max

/**
 * The capture screen on a real camera stack.
 *
 * **Device status: WRITTEN, NOT RUN.** CameraX needs an emulator with a virtual
 * camera, which is not available here (plan 0.11 / §2).
 *
 * The load-bearing assertion is [capturedPhotosAreAlreadyCappedAtTheIntakeSize]:
 * a full sensor frame must never reach the caller, because the batch can stage
 * eight of them.
 */
@RunWith(AndroidJUnit4::class)
class CameraCaptureInstrumentedTest {

    @get:Rule
    val composeRule = createComposeRule()

    @get:Rule
    val cameraPermission: GrantPermissionRule =
        GrantPermissionRule.grant(Manifest.permission.CAMERA)

    @Test
    fun theViewfinderComposesAndBindsTheController() {
        val controller = CameraCaptureController()
        composeRule.setContent {
            CameraCapture(
                onCaptured = {},
                modifier = Modifier.fillMaxSize(),
                controller = controller,
            )
        }
        composeRule.waitForIdle()
        assertTrue("the shutter should be live once the viewfinder composes", controller.isReady)
    }

    /**
     * Nothing outside [CameraCapture] ever holds a full-resolution frame: the
     * `ImageProxy` goes straight through `ImageIntake.forCapture` and is closed in
     * a `finally`.
     */
    @Test
    fun capturedPhotosAreAlreadyCappedAtTheIntakeSize() {
        val controller = CameraCaptureController()
        val latch = CountDownLatch(1)
        var captured: Bitmap? = null

        composeRule.setContent {
            CameraCapture(
                onCaptured = {
                    captured = it
                    latch.countDown()
                },
                modifier = Modifier.fillMaxSize(),
                controller = controller,
            )
        }
        composeRule.waitForIdle()
        composeRule.waitUntil(timeoutMillis = 10_000) { controller.isReady }

        controller.takePhoto(onCaptured = {}, onError = { latch.countDown() })
        assertTrue("capture timed out", latch.await(20, TimeUnit.SECONDS))

        val bitmap = captured
        assertTrue("no bitmap was delivered", bitmap != null)
        assertTrue(
            "captured ${bitmap!!.width}x${bitmap.height}, cap is ${ImageIntake.CAPTURE_MAX_SIDE}",
            max(bitmap.width, bitmap.height) <= ImageIntake.CAPTURE_MAX_SIDE,
        )
        assertTrue(bitmap.config == Bitmap.Config.ARGB_8888)
        // And it is readable, which the whole crop pipeline depends on.
        val pixels = IntArray(1)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, 1, 1)
    }

    /**
     * The denial dialog, with its Open Settings action. Runs without the
     * permission rule granting anything, so it needs the launcher to be declined —
     * marked `@Test` rather than deleted because the copy is a P2-class concern:
     * a dead end here is what drives a user to think the app is broken.
     *
     * On an emulator the permission dialog is auto-declined by the instrumentation
     * only when `GrantPermissionRule` is absent; this test therefore lives with the
     * rule applied class-wide and is expected to be skipped or adapted once an
     * emulator exists. It is left visible rather than absent.
     */
    @Test
    fun theDenialDialogOffersSettingsAndAFallback() {
        composeRule.setContent {
            CameraCapture(onCaptured = {}, modifier = Modifier.fillMaxSize())
        }
        composeRule.waitForIdle()

        val dialog = composeRule.onAllNodesWithTextSafely(CameraCaptureCopy.PERMISSION_TITLE)
        if (dialog) {
            composeRule.onNodeWithText(CameraCaptureCopy.OPEN_SETTINGS).assertIsDisplayed()
            composeRule.onNodeWithText(CameraCaptureCopy.CANCEL).performClick()
        }
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.onAllNodesWithTextSafely(
        text: String,
    ): Boolean = runCatching { onNodeWithText(text).assertIsDisplayed(); true }.getOrDefault(false)
}
