package com.philwoolley.dinnerdecider.vision

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.net.Uri
import android.provider.Settings
import android.util.Log
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageCapture
import androidx.camera.core.ImageCaptureException
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.TextButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.asExecutor
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/**
 * Screen copy for the capture camera.
 *
 * Per-feature copy object rather than `strings.xml`: plan §4 keeps the manifest's
 * `strings.xml` to `app_name` and the permission rationales so fourteen screen
 * steps can be partitioned without contending for one file. The accessibility
 * tradeoff that creates is answered by tests, not by the resource file.
 */
object CameraCaptureCopy {
    const val PERMISSION_TITLE = "Camera access needed"
    const val PERMISSION_MESSAGE =
        "DinnerDecider uses the camera to photograph what is in your fridge. " +
            "Turn on camera access in Settings to take photos, or add them from your gallery instead."
    const val OPEN_SETTINGS = "Open Settings"
    const val CANCEL = "Not now"
    const val PREVIEW_DESCRIPTION = "Camera viewfinder"
}

/**
 * The live camera viewfinder, bound to the composable's own [LocalLifecycleOwner].
 *
 * Port of the iOS capture camera. Three things it must get right:
 *
 * 1. **Bind to the composable's lifecycle**, not the Activity's, so the camera
 *    stops when this screen leaves the composition and does not keep the sensor
 *    (and its buffers) alive behind a navigation.
 * 2. **Never retain a full-resolution frame.** The captured [ImageProxy] goes
 *    straight through [ImageIntake.forCapture] — 2016 px, opaque, software
 *    allocated — before [onCaptured] ever sees it, and the proxy is closed in a
 *    `finally`. A 12 MP frame is ~48 MB as ARGB_8888; a batch of eight of those is
 *    the avoidable pressure `ImageDownscaler`'s KDoc is about.
 * 3. **Degrade, never crash, on a denied permission.** The dialog is the
 *    iOS-equivalent, with an "Open Settings" action, and the app stays fully
 *    usable through gallery import.
 *
 * @param onCaptured receives the already-downscaled bitmap. Called on the main
 *   dispatcher.
 * @param onPermissionDenied invoked when the user declines and dismisses the
 *   dialog, so the host screen can fall back to the gallery picker.
 * @param onError invoked with a capture failure; the viewfinder stays alive.
 */
@Composable
fun CameraCapture(
    onCaptured: (Bitmap) -> Unit,
    modifier: Modifier = Modifier,
    onPermissionDenied: () -> Unit = {},
    onError: (Throwable) -> Unit = {},
    controller: CameraCaptureController = remember { CameraCaptureController() },
) {
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current
    val scope = rememberCoroutineScope()

    var hasPermission by remember { mutableStateOf(hasCameraPermission(context)) }
    var showRationale by remember { mutableStateOf(false) }

    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        hasPermission = granted
        showRationale = !granted
    }

    LaunchedEffect(Unit) {
        if (!hasPermission) permissionLauncher.launch(Manifest.permission.CAMERA)
    }

    controller.bind(
        onCapture = { onImage, onFailure ->
            scope.launch {
                runCatching { captureBitmap(controller.imageCapture) }
                    .onSuccess { bitmap -> if (bitmap != null) onImage(bitmap) }
                    .onFailure(onFailure)
            }
        },
    )

    Box(modifier = modifier.fillMaxSize()) {
        if (hasPermission) {
            AndroidView(
                modifier = Modifier
                    .fillMaxSize()
                    // The viewfinder is decorative to a screen reader: it conveys
                    // nothing a description could carry, and the shutter button the
                    // host screen owns is the actionable element.
                    .clearAndSetSemantics {},
                factory = { viewContext ->
                    PreviewView(viewContext).apply {
                        scaleType = PreviewView.ScaleType.FILL_CENTER
                        implementationMode = PreviewView.ImplementationMode.COMPATIBLE
                    }
                },
                update = { previewView ->
                    bindCamera(
                        context = previewView.context,
                        previewView = previewView,
                        lifecycleOwner = lifecycleOwner,
                        imageCapture = controller.imageCapture,
                        onError = onError,
                    )
                },
            )
        }

        if (showRationale) {
            AlertDialog(
                onDismissRequest = {
                    showRationale = false
                    onPermissionDenied()
                },
                title = { Text(CameraCaptureCopy.PERMISSION_TITLE) },
                text = { Text(CameraCaptureCopy.PERMISSION_MESSAGE) },
                confirmButton = {
                    TextButton(onClick = {
                        showRationale = false
                        openAppSettings(context)
                    }) {
                        Text(CameraCaptureCopy.OPEN_SETTINGS)
                    }
                },
                dismissButton = {
                    TextButton(onClick = {
                        showRationale = false
                        onPermissionDenied()
                    }) {
                        Text(CameraCaptureCopy.CANCEL)
                    }
                },
            )
        }
    }

    DisposableEffect(lifecycleOwner) {
        onDispose {
            // Unbind explicitly as well as relying on the lifecycle, so a fast
            // navigate-away releases the sensor immediately.
            runCatching {
                ProcessCameraProvider.getInstance(context).get().unbindAll()
            }
            controller.unbind()
        }
    }
}

/**
 * The shutter, hoisted so the host screen owns the button and this file owns the
 * camera. [takePhoto] is a no-op until the viewfinder has composed.
 */
class CameraCaptureController {

    internal val imageCapture: ImageCapture = ImageCapture.Builder()
        // MINIMIZE_LATENCY, not MAXIMIZE_QUALITY: the frame is about to be
        // downscaled to 2016 px and then to 896 px for the model, so extra sensor
        // processing buys nothing and costs both time and peak memory.
        .setCaptureMode(ImageCapture.CAPTURE_MODE_MINIMIZE_LATENCY)
        .build()

    private var capture: ((onImage: (Bitmap) -> Unit, onFailure: (Throwable) -> Unit) -> Unit)? = null

    internal fun bind(onCapture: (onImage: (Bitmap) -> Unit, onFailure: (Throwable) -> Unit) -> Unit) {
        capture = onCapture
    }

    internal fun unbind() {
        capture = null
    }

    /** True once the viewfinder is composed and a photo can actually be taken. */
    val isReady: Boolean get() = capture != null

    fun takePhoto(onCaptured: (Bitmap) -> Unit, onError: (Throwable) -> Unit = {}) {
        capture?.invoke(onCaptured, onError)
    }
}

// ---------------------------------------------------------------------------

internal fun hasCameraPermission(context: Context): Boolean =
    ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) ==
        PackageManager.PERMISSION_GRANTED

internal fun openAppSettings(context: Context) {
    runCatching {
        context.startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
                data = Uri.fromParts("package", context.packageName, null)
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            },
        )
    }.onFailure { Log.w(TAG, "openAppSettings: no settings activity", it) }
}

private fun bindCamera(
    context: Context,
    previewView: PreviewView,
    lifecycleOwner: LifecycleOwner,
    imageCapture: ImageCapture,
    onError: (Throwable) -> Unit,
) {
    val future = ProcessCameraProvider.getInstance(context)
    future.addListener({
        runCatching {
            val provider = future.get()
            val preview = Preview.Builder().build().apply {
                surfaceProvider = previewView.surfaceProvider
            }
            provider.unbindAll()
            provider.bindToLifecycle(
                lifecycleOwner,
                CameraSelector.DEFAULT_BACK_CAMERA,
                preview,
                imageCapture,
            )
        }.onFailure {
            Log.w(TAG, "bindCamera: could not bind the camera", it)
            onError(it)
        }
    }, ContextCompat.getMainExecutor(context))
}

/**
 * Take one photo and hand back a bitmap that is **already** capped at
 * [ImageIntake.CAPTURE_MAX_SIDE].
 *
 * The [ImageProxy] is converted and closed inside a `finally` on a background
 * dispatcher, and the full-resolution bitmap is released as soon as
 * [ImageIntake.forCapture] has produced the downscaled one. Nothing outside this
 * function ever holds a full sensor frame.
 */
private suspend fun captureBitmap(imageCapture: ImageCapture): Bitmap? =
    withContext(Dispatchers.Default) {
        val proxy = suspendCancellableCoroutine { continuation ->
            imageCapture.takePicture(
                Dispatchers.Default.asExecutor(),
                object : ImageCapture.OnImageCapturedCallback() {
                    override fun onCaptureSuccess(image: ImageProxy) {
                        continuation.resume(image)
                    }

                    override fun onError(exception: ImageCaptureException) {
                        continuation.resumeWithException(exception)
                    }
                },
            )
        }

        try {
            val full = proxy.toBitmap()
            val upright = ImageIntake.withRotationBaked(full, proxy.imageInfo.rotationDegrees)
            if (upright !== full) full.recycle()
            val captured = ImageIntake.forCapture(upright)
            if (captured !== upright) upright.recycle()
            captured
        } finally {
            // Not closing the proxy starves CameraX's buffer queue and the next
            // capture never fires.
            proxy.close()
        }
    }

private const val TAG = "CameraCapture"
