package com.philwoolley.dinnerdecider.model

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import com.philwoolley.dinnerdecider.contract.ModelDownloadLogic
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.nio.channels.FileChannel
import kotlin.coroutines.coroutineContext

/** What an import attempt produced. */
sealed interface ImportResult {
    /**
     * The file was copied, verified and renamed into `filesDir/models`.
     *
     * @param matchedPinnedDigest true when the file is one of the two shipped files
     *   and its digest matched the compiled-in value. False for a fine-tuned or
     *   otherwise unknown GGUF, where [sha256] is a record of what was copied rather
     *   than a match against something we already knew.
     */
    data class Success(
        val file: File,
        val sha256: String,
        val matchedPinnedDigest: Boolean,
    ) : ImportResult

    /** The picked file cannot be a model file at all. Nothing was copied. */
    data class Rejected(val reason: String) : ImportResult

    /** The copy or the verification did not complete. Anything partial was removed. */
    data class Failed(val reason: String) : ImportResult
}

/**
 * Imports a user-supplied `.gguf` through the Storage Access Framework.
 *
 * ## Everything lands in `filesDir`
 *
 * The copy goes into `filesDir/models` and nowhere else, which is what makes the
 * imported file trustworthy afterwards: `ModelFileLocator` accepts that directory on
 * location precisely because no other app can write to it. Importing in place, or
 * importing to external storage, would hand the locator a file it then has to refuse.
 *
 * ## The dev path for the staged weights
 *
 * A developer with the staged pair in the repo's gitignored `models/` directory does
 * not need this screen. Push them straight to the external app directory:
 *
 * ```
 * adb push models/gemma-4-E4B-it-UD-IQ3_XXS.gguf \
 *   /sdcard/Android/data/com.philwoolley.dinnerdecider/files/models/
 * adb push models/mmproj-gemma-4-E4B-it-Q8_0.gguf \
 *   /sdcard/Android/data/com.philwoolley.dinnerdecider/files/models/
 * ```
 *
 * [ModelFileLocator] accepts that directory **only** with the developer toggle on
 * **and** after a digest check, and both staged files are compiled-in pinned digests
 * so they verify with no further setup. Any other GGUF has no trusted digest out
 * there and must come through this importer instead, which copies it somewhere safe
 * and records what it hashed.
 *
 * ## Why the digest is a second pass
 *
 * [FileChannel.transferTo] moves bytes kernel-side, so they never pass through this
 * process and cannot be fed to a digest on the way past. Hashing is therefore a
 * separate read of the copied file. For 3.46 GB that is a real cost, and it is the
 * right one: the alternative is a user-space copy loop that is slower for the whole
 * transfer, not just the verification.
 */
class ModelImporter(
    private val contentResolver: ContentResolver,
    private val modelsDir: File,
    private val trustedDigests: TrustedDigests = TrustedDigests(modelsDir),
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
    private val freeDiskProbe: () -> Long = { modelsDir.usableSpace },
) {

    /**
     * Import the document at [uri].
     *
     * @param onProgress cumulative copied bytes and the declared total, or 0 when the
     *   provider does not report a size.
     */
    suspend fun import(
        uri: Uri,
        onProgress: (copied: Long, total: Long) -> Unit = { _, _ -> },
    ): ImportResult = withContext(ioDispatcher) {
        val displayName = queryDisplayName(uri)
            ?: return@withContext ImportResult.Rejected(
                "That file has no name we can read. Pick the .gguf file from your Files app."
            )

        rejectionFor(displayName)?.let { return@withContext ImportResult.Rejected(it) }

        val descriptor = runCatching { contentResolver.openFileDescriptor(uri, "r") }.getOrNull()
            ?: return@withContext ImportResult.Failed(
                "That file could not be opened. It may have been moved or deleted."
            )

        descriptor.use { pfd ->
            val declaredSize = pfd.statSize.takeIf { it > 0 } ?: 0L
            val free = runCatching { freeDiskProbe() }.getOrDefault(0L)
            if (declaredSize > 0 && free > 0 &&
                !ModelDownloadLogic.hasEnoughDiskSpace(freeBytes = free, required = declaredSize)
            ) {
                return@withContext ImportResult.Failed(
                    "Not enough free space to copy $displayName. Free up about " +
                        "${String.format("%.1f", declaredSize / 1_000_000_000.0)} GB and try again."
                )
            }

            copyAndVerify(
                fileName = displayName,
                declaredSize = declaredSize,
                openSourceChannel = { java.io.FileInputStream(pfd.fileDescriptor).channel },
                onProgress = onProgress,
            )
        }
    }

    /**
     * The half of the import that touches only files, exposed so it can be proved on
     * the host JVM with no `ContentResolver` and no Robolectric.
     *
     * Copies into `<name>.part`, verifies, then renames. A file that fails
     * verification is deleted, never left where the locator could pick it up.
     */
    suspend fun copyAndVerify(
        fileName: String,
        declaredSize: Long,
        openSourceChannel: () -> FileChannel,
        onProgress: (copied: Long, total: Long) -> Unit = { _, _ -> },
    ): ImportResult {
        rejectionFor(fileName)?.let { return ImportResult.Rejected(it) }

        modelsDir.mkdirs()
        val part = File(modelsDir, fileName + ModelFileDownloader.PART_SUFFIX)
        val destination = File(modelsDir, fileName)

        val copied = try {
            transfer(part, declaredSize, openSourceChannel, onProgress)
        } catch (error: IOException) {
            part.delete()
            return ImportResult.Failed(
                "Copying $fileName stopped partway (${error.message ?: "read error"}). " +
                    "Try importing it again."
            )
        }

        if (copied <= 0L) {
            part.delete()
            return ImportResult.Failed("$fileName came through empty. Try importing it again.")
        }

        val expectedSize = trustedDigests.exactBytesFor(fileName)
        if (expectedSize != null && !ModelDownloadLogic.sizeIsValid(copied, expectedSize)) {
            part.delete()
            return ImportResult.Failed(
                "$fileName is not the size this app expects, so it was not imported. " +
                    "Check you picked the right file."
            )
        }

        val digest = ModelFileLocator.sha256Of(part)
        if (digest == null) {
            part.delete()
            return ImportResult.Failed("$fileName could not be checked after copying. Try again.")
        }

        // A name we have a compiled-in digest for must match it exactly. This is the
        // one place an import can be wrong in a way the user cannot see.
        val pinned = trustedDigests.pinnedDigestFor(fileName)
        if (pinned != null && !digest.equals(pinned, ignoreCase = true)) {
            part.delete()
            return ImportResult.Failed(
                "$fileName did not match its security check, so it was not imported. " +
                    "Download it again from the original source."
            )
        }

        if (destination.exists() && !destination.delete()) {
            part.delete()
            return ImportResult.Failed("Could not replace the existing $fileName. Try again.")
        }
        if (!part.renameTo(destination)) {
            part.delete()
            return ImportResult.Failed("Could not save $fileName into the app's storage. Try again.")
        }

        // Record what we hashed, for a file we copied ourselves into a directory no
        // other app can write. That is an integrity record, not a claim that the file
        // is genuine: its authenticity rests on the user having deliberately picked it.
        trustedDigests.record(fileName, digest)

        return ImportResult.Success(
            file = destination,
            sha256 = digest,
            matchedPinnedDigest = pinned != null,
        )
    }

    private suspend fun transfer(
        part: File,
        declaredSize: Long,
        openSourceChannel: () -> FileChannel,
        onProgress: (Long, Long) -> Unit,
    ): Long {
        openSourceChannel().use { source ->
            FileOutputStream(part).use { sink ->
                val sinkChannel = sink.channel
                val sourceSize = runCatching { source.size() }.getOrDefault(-1L)

                if (sourceSize <= 0L) {
                    // A provider serving a pipe has no seekable channel, so transferTo
                    // is not available. Fall back to an ordinary stream copy.
                    return streamCopy(source, sink, declaredSize, onProgress)
                }

                var position = 0L
                while (position < sourceSize) {
                    coroutineContext.ensureActive()
                    val chunk = minOf(TRANSFER_CHUNK_BYTES, sourceSize - position)
                    val moved = source.transferTo(position, chunk, sinkChannel)
                    if (moved <= 0L) break
                    position += moved
                    onProgress(position, if (declaredSize > 0) declaredSize else sourceSize)
                }
                sink.flush()
                sink.fd.sync()
                return position
            }
        }
    }

    private suspend fun streamCopy(
        source: FileChannel,
        sink: FileOutputStream,
        declaredSize: Long,
        onProgress: (Long, Long) -> Unit,
    ): Long {
        val input = java.nio.channels.Channels.newInputStream(source)
        val buffer = ByteArray(STREAM_BUFFER_BYTES)
        var copied = 0L
        while (true) {
            coroutineContext.ensureActive()
            val read = input.read(buffer)
            if (read <= 0) break
            sink.write(buffer, 0, read)
            copied += read
            onProgress(copied, declaredSize)
        }
        sink.flush()
        sink.fd.sync()
        return copied
    }

    /** The provider's display name for [uri], or null. */
    fun queryDisplayName(uri: Uri): String? = runCatching {
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)
            ?.use { cursor ->
                val index = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (index >= 0 && cursor.moveToFirst()) cursor.getString(index) else null
            }
            ?: uri.lastPathSegment?.substringAfterLast('/')
    }.getOrNull()

    /** Why [fileName] cannot be imported, or null when it can. */
    private fun rejectionFor(fileName: String): String? = when {
        !isSafeFileName(fileName) ->
            "That file name cannot be used. Rename it and try again."

        !fileName.lowercase().endsWith(".gguf") ->
            "That is not a .gguf model file. Pick the weights or the mmproj projector."

        ModelFileSelection.isMmproj(fileName) -> null

        !fileName.startsWith(ModelFileSelection.MODEL_PREFIX) ->
            "This app loads weights named ${ModelFileSelection.MODEL_PREFIX}....gguf and a " +
                "projector named mmproj....gguf. Rename the file to match, or download it in " +
                "the app instead."

        else -> null
    }

    companion object {

        private const val TRANSFER_CHUNK_BYTES = 32L * 1024 * 1024
        private const val STREAM_BUFFER_BYTES = 1 shl 16

        /** Build a [ModelImporter] for a real [Context]. */
        fun create(context: Context): ModelImporter {
            val appContext = context.applicationContext
            val dir = ModelDirs.internalModelsDir(appContext).apply { mkdirs() }
            return ModelImporter(
                contentResolver = appContext.contentResolver,
                modelsDir = dir,
                trustedDigests = TrustedDigests(dir),
            )
        }

        /**
         * The picker intent.
         *
         * `ACTION_OPEN_DOCUMENT`, not `ACTION_GET_CONTENT`: the user picks a specific
         * document from a provider they chose, and the grant is scoped to that one
         * document. The MIME filter is the wildcard because **GGUF has no registered
         * MIME type**; a narrower filter would hide the file the user is looking for on
         * most providers, which is a worse failure than showing too much.
         */
        fun openDocumentIntent(): Intent = Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
            addCategory(Intent.CATEGORY_OPENABLE)
            type = "*/*"
        }

        /**
         * Whether a provider-supplied display name is safe to build a path from.
         *
         * The name comes from a content provider, so it is attacker-influenced, and it
         * is concatenated onto `filesDir/models`. A name containing a separator or a
         * parent reference would write outside the models directory. Checked before
         * anything is opened.
         */
        fun isSafeFileName(name: String): Boolean {
            if (name.isBlank()) return false
            if (name.length > 255) return false
            if (name == "." || name == "..") return false
            if (name.startsWith(".")) return false
            return name.none { it == '/' || it == '\\' || it.isWhitespace() || it.isISOControl() }
        }
    }
}
