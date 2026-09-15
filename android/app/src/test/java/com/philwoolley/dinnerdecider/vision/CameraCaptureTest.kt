package com.philwoolley.dinnerdecider.vision

import android.Manifest
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * Host-JVM cover for the parts of the capture screen that are not the camera:
 * the permission gate and the denial copy.
 *
 * The viewfinder, the CameraX binding and the dialog's rendering need a device;
 * `CameraCaptureInstrumentedTest` covers those. Compose UI testing is not
 * available on the host JVM here — `compose-ui-test-junit4` is an
 * `androidTestImplementation` dependency — so this file deliberately stays at the
 * level of plain functions and constants.
 */
@RunWith(RobolectricTestRunner::class)
class CameraCaptureTest {

    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test
    fun thePermissionGateReflectsTheGrantedState() {
        val application = shadowOf(ApplicationProvider.getApplicationContext<android.app.Application>())

        application.denyPermissions(Manifest.permission.CAMERA)
        assertFalse(hasCameraPermission(context))

        application.grantPermissions(Manifest.permission.CAMERA)
        assertTrue(hasCameraPermission(context))
    }

    /**
     * The denial path must be a real offer, not a dead end. The iOS-equivalent
     * dialog explains *why* the camera is wanted, gives an "Open Settings" action,
     * and names the gallery as the way to keep going — because a denied camera
     * permission leaves the app fully usable.
     */
    @Test
    fun theDenialCopyExplainsWhyOffersSettingsAndNamesTheFallback() {
        assertEquals("Camera access needed", CameraCaptureCopy.PERMISSION_TITLE)
        assertEquals("Open Settings", CameraCaptureCopy.OPEN_SETTINGS)
        assertTrue(CameraCaptureCopy.PERMISSION_MESSAGE.contains("fridge"))
        assertTrue(CameraCaptureCopy.PERMISSION_MESSAGE.contains("Settings"))
        assertTrue(CameraCaptureCopy.PERMISSION_MESSAGE.contains("gallery"))
        assertTrue(CameraCaptureCopy.CANCEL.isNotBlank())
    }

    @Test
    fun theCopyCarriesNoEmDashMatchingTheRestOfTheAppsVoice() {
        listOf(
            CameraCaptureCopy.PERMISSION_TITLE,
            CameraCaptureCopy.PERMISSION_MESSAGE,
            CameraCaptureCopy.OPEN_SETTINGS,
            CameraCaptureCopy.CANCEL,
            CameraCaptureCopy.PREVIEW_DESCRIPTION,
        ).forEach { line ->
            assertFalse("em dash in: $line", line.contains('—'))
        }
    }

    @Test
    fun openAppSettingsTargetsThisAppsDetailsPage() {
        openAppSettings(context)

        val started = shadowOf(ApplicationProvider.getApplicationContext<android.app.Application>())
            .nextStartedActivity
        assertEquals(android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS, started.action)
        assertEquals(context.packageName, started.data?.schemeSpecificPart)
    }

    /**
     * The shutter is a no-op before the viewfinder has composed, rather than a
     * crash: the host screen's button exists from the first frame.
     */
    @Test
    fun theControllerIsNotReadyUntilTheViewfinderBindsIt() {
        val controller = CameraCaptureController()
        assertFalse(controller.isReady)

        var captured = false
        var errored = false
        controller.takePhoto(onCaptured = { captured = true }, onError = { errored = true })
        assertFalse(captured)
        assertFalse(errored)
    }
}
