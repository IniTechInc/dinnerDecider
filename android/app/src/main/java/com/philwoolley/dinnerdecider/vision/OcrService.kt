package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.util.Log
import com.google.mlkit.vision.common.InputImage
import com.google.mlkit.vision.text.Text
import com.google.mlkit.vision.text.TextRecognition
import com.google.mlkit.vision.text.TextRecognizer
import com.google.mlkit.vision.text.latin.TextRecognizerOptions
import kotlinx.coroutines.suspendCancellableCoroutine
import java.io.Closeable
import kotlin.coroutines.resume

/**
 * Reads packaging text off a crop so it can be fused into the model prompt.
 *
 * Port of `DinnerDecider/Services/Vision/OCRService.swift`. Image + OCR text beats
 * image-only for fine-grained packaged-grocery recognition — the "look-alike
 * products" failure mode (`OCRService.swift:5-8`). Runs entirely on-device.
 *
 * ## Bundled, not Play-Services
 *
 * [TextRecognizerOptions.DEFAULT_OPTIONS] comes from `com.google.mlkit:text-recognition`,
 * whose Latin model ships **inside the APK**. The `com.google.android.gms:play-services-mlkit-text-recognition`
 * variant downloads its model on first use, which means the very first scan after
 * install — offline, or on a phone that has never reached Google Play Services —
 * would silently return nothing. An app whose entire premise is 100% on-device
 * inference cannot have an OCR stage that needs the network to warm up. The ~22 MB
 * of bundled models is the price, and it is already in the APK size budget.
 *
 * ## The contract, which is narrower than it looks
 *
 * Iterate blocks, then their lines; take `line.text`; join with a **single space**.
 *
 * - **No confidence filtering.** iOS takes `topCandidates(1).first` and keeps it
 *   whatever its confidence. A threshold here would drop exactly the smudged
 *   label text OCR was added for.
 * - **No newlines and no reordering.** The result is one flat string in the
 *   detector's own order. `shared/pipeline/scan-pipeline.md` Stage 3.
 * - **Never throws.** Any failure yields `""` and the scan continues image-only.
 *   At the prompt boundary an empty string becomes the literal `none`
 *   (`VisionPrompts`), so "OCR failed" and "there was no text" are deliberately
 *   the same thing to the model.
 */
class OcrService(
    private val recognizer: TextRecognizer =
        TextRecognition.getClient(TextRecognizerOptions.DEFAULT_OPTIONS),
) : Closeable {

    /**
     * Recognized text as one space-joined line, or `""` for no text, an
     * unreadable bitmap, or any failure at all.
     */
    suspend fun recognizeText(bitmap: Bitmap): String {
        val readable = ImageIntake.ensureSoftware(bitmap) ?: return ""
        return runCatching {
            val image = InputImage.fromBitmap(readable, 0)
            val text = suspendCancellableCoroutine { continuation ->
                recognizer.process(image)
                    .addOnSuccessListener { continuation.resume(it) }
                    .addOnFailureListener {
                        Log.w(TAG, "recognizeText: OCR failed; continuing image-only", it)
                        continuation.resume(null)
                    }
                    .addOnCanceledListener { continuation.resume(null) }
            }
            text?.let(::flatten).orEmpty()
        }.onFailure { Log.w(TAG, "recognizeText: unexpected failure", it) }.getOrDefault("")
    }

    override fun close() {
        runCatching { recognizer.close() }
    }

    companion object {
        private const val TAG = "OcrService"

        /**
         * Blocks -> lines -> `line.text`, in the detector's own order.
         *
         * The flattening is a one-liner precisely because every judgement call is
         * in [joinLines]; keeping them apart is also what lets the join rule be
         * covered on the host JVM, since ML Kit's `Text` cannot be constructed or
         * mocked off-device.
         */
        fun flatten(text: Text): String =
            joinLines(text.textBlocks.flatMap { block -> block.lines.map { it.text } })

        /**
         * The join rule, as a pure function: **single space**, order preserved, no
         * confidence filtering, no newlines.
         *
         * Empty line texts are dropped so the join can never emit a double space,
         * which would change the prompt's character count for no gain.
         */
        fun joinLines(lines: List<String>): String =
            lines.filter { it.isNotEmpty() }.joinToString(" ")
    }
}
