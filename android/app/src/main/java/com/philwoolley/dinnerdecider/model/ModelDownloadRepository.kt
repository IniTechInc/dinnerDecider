package com.philwoolley.dinnerdecider.model

import android.content.Context
import android.net.ConnectivityManager
import com.philwoolley.dinnerdecider.contract.FileDownloadProgress
import com.philwoolley.dinnerdecider.contract.ModelDownloadFile
import com.philwoolley.dinnerdecider.contract.ModelDownloadLogic
import com.philwoolley.dinnerdecider.contract.ModelDownloadState
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.security.MessageDigest
import java.util.concurrent.TimeUnit
import kotlin.coroutines.coroutineContext

/**
 * One remote file, as the transfer engine needs to see it.
 *
 * [ModelFileDownloader] works on this rather than on [ModelDownloadFile] directly so
 * the engine is testable against a MockWebServer with a handful of bytes. The real
 * pair's sizes are 534 MB and 3.46 GB, which no unit test is going to produce.
 */
data class RemoteFileSpec(
    val fileName: String,
    val url: String,
    /** Exact expected byte count. Checked, alongside [sha256], before the file is accepted. */
    val exactBytes: Long,
    /** Expected lowercase-hex SHA-256. */
    val sha256: String,
    val displayName: String,
)

/** The spec for one of the two shipped files, optionally pointed at a different host. */
fun ModelDownloadFile.toSpec(urlOverride: String? = null): RemoteFileSpec = RemoteFileSpec(
    fileName = fileName,
    url = urlOverride ?: remoteUrl,
    exactBytes = exactBytes,
    sha256 = sha256,
    displayName = displayName,
)

/** What one file's transfer produced. */
sealed interface DownloadOutcome {
    /** The bytes verified and were renamed into place. */
    data class Success(val file: File) : DownloadOutcome

    /**
     * The transfer stopped.
     *
     * @param retryable whether trying again could plausibly work. A dropped
     *   connection is retryable; a 404 on the Hugging Face URL is not, and telling
     *   the user to tap Retry forever would be a lie.
     * @param partialKept whether a `.part` file survived, so a retry resumes rather
     *   than restarting. False whenever the partial was untrustworthy.
     */
    data class Failure(
        val reason: String,
        val retryable: Boolean,
        val partialKept: Boolean,
    ) : DownloadOutcome
}

/**
 * The transfer engine: resumable, integrity-checked, and with no decision logic of
 * its own beyond HTTP mechanics.
 *
 * ## Resume
 * A partial download lives at `<name>.part` and is resumed with
 * `Range: bytes=N-`. Three responses mean the resume did not happen the way we
 * asked, and each one **restarts cleanly rather than corrupting**:
 *
 * - **`200 OK` in response to a Range request.** The server ignored the header and
 *   is sending the whole file from byte zero. Appending that to an existing partial
 *   produces a file of the right length made of the wrong bytes. That is the failure
 *   this class exists to prevent, and it would not be caught by a length check - only
 *   by the digest, and only after 3.46 GB of wasted transfer.
 * - **`416 Range Not Satisfiable`.** The partial is longer than the resource, or the
 *   server will not serve the range at all.
 * - **A `Content-Range` whose start is not where we asked to continue.**
 *
 * A clean restart is attempted once. A server that ignores the range twice is
 * reported rather than looped on.
 *
 * ## Integrity
 * SHA-256 is computed **while writing**, then checked against both the digest and
 * the exact byte size from `shared/runtime/model-files.md`. On a resume the existing
 * partial is fed through the same digest first, so the hash always covers the whole
 * file and never just the appended tail. Only then is the file renamed into
 * `filesDir/models`.
 *
 * This is **stricter than iOS**, which checked `Content-Length` only. The extra
 * strictness is deliberate and is kept: these bytes are handed to llama.cpp's native
 * GGUF parser, so "we cannot vouch for them" has to mean "we do not load them".
 *
 * ## Atomicity
 * The rename is within one directory on one filesystem, so it is atomic. Nothing
 * ever observes a half-written file under the real name, which matters because
 * `ModelFileLocator` trusts `filesDir` on location.
 */
class ModelFileDownloader(
    private val client: OkHttpClient,
    private val modelsDir: File,
    private val trustedDigests: TrustedDigests = TrustedDigests(modelsDir),
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) {

    /**
     * Fetch [spec] into [modelsDir], resuming any existing partial.
     *
     * @param onProgress called with cumulative received bytes and the best known
     *   total. Called often; keep it cheap.
     */
    suspend fun download(
        spec: RemoteFileSpec,
        onProgress: (received: Long, total: Long) -> Unit = { _, _ -> },
    ): DownloadOutcome = withContext(ioDispatcher) {
        modelsDir.mkdirs()
        transferWithRestarts(spec, onProgress)
    }

    private suspend fun transferWithRestarts(
        spec: RemoteFileSpec,
        onProgress: (Long, Long) -> Unit,
    ): DownloadOutcome {
        val destination = File(modelsDir, spec.fileName)
        val part = File(modelsDir, spec.fileName + PART_SUFFIX)

        var restarts = 0
        while (true) {
            when (val step = transferOnce(spec, part, onProgress)) {
                is Step.Complete -> return finish(spec, part, destination)
                is Step.Stopped -> return step.outcome
                is Step.RestartFromZero -> {
                    part.delete()
                    restarts += 1
                    if (restarts > 1) {
                        return DownloadOutcome.Failure(
                            reason = "The download server kept sending ${spec.displayName} from the " +
                                "beginning instead of continuing where it stopped. Try again later.",
                            retryable = true,
                            partialKept = false,
                        )
                    }
                }
            }
        }
    }

    private sealed interface Step {
        data object Complete : Step
        data object RestartFromZero : Step
        data class Stopped(val outcome: DownloadOutcome.Failure) : Step
    }

    private suspend fun transferOnce(
        spec: RemoteFileSpec,
        part: File,
        onProgress: (Long, Long) -> Unit,
    ): Step {
        val existing = if (part.isFile) part.length() else 0L

        // A partial longer than the resource cannot be a prefix of it. Restart rather
        // than send a Range the server will reject anyway.
        if (existing > spec.exactBytes) return Step.RestartFromZero

        val request = Request.Builder()
            .url(spec.url)
            .apply { if (existing > 0L) header("Range", "bytes=$existing-") }
            .build()

        val response = try {
            client.newCall(request).execute()
        } catch (error: IOException) {
            return Step.Stopped(
                DownloadOutcome.Failure(
                    reason = networkMessage(spec, error),
                    retryable = true,
                    partialKept = part.isFile,
                )
            )
        }

        response.use {
            val code = response.code

            // The server ignored the range and is replaying the whole file.
            if (existing > 0L && code == 200) return Step.RestartFromZero
            if (code == 416) return Step.RestartFromZero
            if (code != 200 && code != 206) {
                return Step.Stopped(
                    DownloadOutcome.Failure(
                        reason = httpMessage(spec, code),
                        retryable = code == 429 || code >= 500,
                        partialKept = part.isFile,
                    )
                )
            }

            val contentRange = response.header("Content-Range")
            if (existing > 0L) {
                val start = parseContentRangeStart(contentRange)
                // A 206 that resumes from somewhere other than where we asked would
                // leave a hole. Restart instead of writing one.
                if (start != null && start != existing) return Step.RestartFromZero
            }

            val body = response.body ?: return Step.Stopped(
                DownloadOutcome.Failure(
                    reason = "${spec.displayName} came back with no content. Tap Retry.",
                    retryable = true,
                    partialKept = part.isFile,
                )
            )

            val declared = body.contentLength()
            val total = parseContentRangeTotal(contentRange)
                ?: if (declared > 0L) existing + declared else spec.exactBytes

            val digest = MessageDigest.getInstance("SHA-256")
            if (existing > 0L && !feedExistingInto(digest, part)) {
                // We could not re-read our own partial, so we cannot hash the whole
                // file. Start over rather than accept an incomplete digest.
                return Step.RestartFromZero
            }

            var received = existing
            onProgress(received, total)

            try {
                FileOutputStream(part, existing > 0L).use { sink ->
                    body.byteStream().use { source ->
                        val buffer = ByteArray(TRANSFER_BUFFER_BYTES)
                        while (true) {
                            coroutineContext.ensureActive()
                            val read = source.read(buffer)
                            if (read <= 0) break
                            sink.write(buffer, 0, read)
                            digest.update(buffer, 0, read)
                            received += read
                            onProgress(received, total)
                        }
                    }
                    sink.flush()
                    sink.fd.sync()
                }
            } catch (error: IOException) {
                // Keep the partial: this is exactly the case resume exists for.
                return Step.Stopped(
                    DownloadOutcome.Failure(
                        reason = networkMessage(spec, error),
                        retryable = true,
                        partialKept = part.isFile,
                    )
                )
            }

            lastDigestHex = digest.digest().toHex()
            return Step.Complete
        }
    }

    /**
     * Size, then digest, then atomic rename.
     *
     * A file that fails either check is **deleted**, not left for a resume to append
     * to: its bytes are already known to be wrong, so resuming would only build a
     * longer wrong file.
     */
    private fun finish(spec: RemoteFileSpec, part: File, destination: File): DownloadOutcome {
        val actualSize = part.length()
        if (!ModelDownloadLogic.sizeIsValid(actual = actualSize, expected = spec.exactBytes)) {
            part.delete()
            return DownloadOutcome.Failure(
                reason = "${spec.displayName} arrived incomplete, so it was removed. " +
                    "Tap Retry to download it again.",
                retryable = true,
                partialKept = false,
            )
        }

        val actualDigest = lastDigestHex
        if (actualDigest == null || !actualDigest.equals(spec.sha256, ignoreCase = true)) {
            part.delete()
            return DownloadOutcome.Failure(
                reason = "${spec.displayName} did not match its security check, so it was removed. " +
                    "Tap Retry to download it again.",
                retryable = true,
                partialKept = false,
            )
        }

        if (destination.exists() && !destination.delete()) {
            return DownloadOutcome.Failure(
                reason = "Could not replace the existing ${spec.displayName}. " +
                    "Free up some space and tap Retry.",
                retryable = true,
                partialKept = true,
            )
        }
        if (!part.renameTo(destination)) {
            return DownloadOutcome.Failure(
                reason = "Could not save ${spec.displayName} to the app's storage. Tap Retry.",
                retryable = true,
                partialKept = true,
            )
        }

        // Bookkeeping for the locator. A no-op for the pinned pair, whose digests are
        // compiled in and must not be shadowable by a recorded entry.
        trustedDigests.record(spec.fileName, actualDigest)
        return DownloadOutcome.Success(destination)
    }

    /** Digest of the most recently completed transfer body. Single-file-at-a-time by design. */
    private var lastDigestHex: String? = null

    private fun feedExistingInto(digest: MessageDigest, part: File): Boolean = runCatching {
        part.inputStream().use { stream ->
            val buffer = ByteArray(TRANSFER_BUFFER_BYTES)
            while (true) {
                val read = stream.read(buffer)
                if (read <= 0) break
                digest.update(buffer, 0, read)
            }
        }
        true
    }.getOrDefault(false)

    private fun networkMessage(spec: RemoteFileSpec, error: IOException): String {
        val detail = error.message?.takeIf { it.isNotBlank() }
        return if (detail == null) {
            "The connection dropped while downloading ${spec.displayName}. " +
                "Reconnect and tap Retry to continue where it stopped."
        } else {
            "The connection dropped while downloading ${spec.displayName} ($detail). " +
                "Reconnect and tap Retry to continue where it stopped."
        }
    }

    private fun httpMessage(spec: RemoteFileSpec, code: Int): String = when {
        code == 404 -> "${spec.displayName} is no longer at its download address. " +
            "Update the app, or copy the file across from a computer instead."
        code == 429 -> "The download server asked us to slow down. Try again in a few minutes."
        code >= 500 -> "The download server is having trouble (error $code). Try again in a moment."
        else -> "The download server refused the request (error $code)."
    }

    companion object {
        /** Suffix for an in-flight transfer. Never resolved by `ModelFileLocator`, which filters on `.gguf`. */
        const val PART_SUFFIX: String = ".part"

        private const val TRANSFER_BUFFER_BYTES = 1 shl 16

        /** `bytes 1024-2047/4096` -> 1024. Null when absent or unparseable. */
        fun parseContentRangeStart(header: String?): Long? {
            val value = header?.trim() ?: return null
            val range = value.removePrefix("bytes").trim().substringBefore('/').trim()
            return range.substringBefore('-').trim().toLongOrNull()
        }

        /** `bytes 1024-2047/4096` -> 4096. Null for `*` or an absent header. */
        fun parseContentRangeTotal(header: String?): Long? {
            val value = header?.trim() ?: return null
            return value.substringAfter('/', "").trim().toLongOrNull()
        }

        /**
         * An OkHttp client shaped for multi-gigabyte transfers.
         *
         * `callTimeout(0)` is load-bearing: OkHttp's call timeout covers the whole
         * exchange including the body, so any finite value is a hard ceiling on how
         * long a 3.46 GB download may take. The read timeout still catches a stalled
         * connection, which is the failure a timeout should actually detect.
         */
        fun defaultClient(): OkHttpClient = OkHttpClient.Builder()
            .callTimeout(0, TimeUnit.MILLISECONDS)
            .connectTimeout(30, TimeUnit.SECONDS)
            .readTimeout(60, TimeUnit.SECONDS)
            .retryOnConnectionFailure(true)
            .build()

        private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
    }
}

/**
 * Everything the model setup screen renders, in one snapshot.
 *
 * The lifecycle [state] is only ever changed through [ModelDownloadLogic.next], so
 * the screen's five states and every transition between them are the ones the
 * host-JVM state-machine tests already pin.
 */
data class ModelDownloadUiState(
    val state: ModelDownloadState = ModelDownloadState.NotStarted,
    val progress: Map<ModelDownloadFile, FileDownloadProgress> = emptyMap(),
    val currentFile: ModelDownloadFile? = null,
    val bytesPerSecond: Double = 0.0,
    val isMetered: Boolean = false,
    /** Set when a start was refused because the connection is metered. */
    val needsMeteredConfirmation: Boolean = false,
) {
    /** Combined byte progress across both files, approximate totals filled in. */
    val overall: FileDownloadProgress
        get() {
            var received = 0L
            var total = 0L
            for (file in ModelDownloadFile.entries) {
                val fileProgress = progress[file] ?: FileDownloadProgress()
                received += fileProgress.bytesReceived
                total += if (fileProgress.totalBytes > 0) fileProgress.totalBytes else file.approxBytes
            }
            return FileDownloadProgress(received, total)
        }

    /** Overall fraction, through the contract helper so the UI cannot invent its own. */
    val overallFraction: Double
        get() = ModelDownloadLogic.overallFraction(overall.bytesReceived, overall.totalBytes)
}

/**
 * Owns the model download's state and drives the transfer.
 *
 * ## The job holds no decision logic
 * Every gate and every transition routes through the pure [ModelDownloadLogic]: the
 * 4.5 GB disk precheck, the metered confirmation, `sizeIsValid`, `overallFraction`
 * and the whole `next(state, event)` table. This class supplies I/O and sequencing
 * and nothing else, which is what keeps the state machine provable on the host JVM
 * with no network, no JobScheduler and no filesystem.
 *
 * ## Process singleton, on purpose
 * The JobService, the WorkManager verification worker and the setup screen's
 * ViewModel all have to observe and mutate the same state. [get] is how they find
 * each other. Plan Step 47's service container may later hand this instance out
 * instead; nothing here assumes it is the one doing the construction.
 *
 * ## mmproj first
 * [ModelDownloadFile.entries] is declared in download order and iterated in it. The
 * 534 MB projector finishing first gives the user a fast, encouraging success before
 * the 3.46 GB weights start. That is deliberate, not incidental, and reordering the
 * enum would silently undo it.
 */
class ModelDownloadRepository(
    private val locator: ModelFileLocator,
    private val downloader: ModelFileDownloader,
    private val scheduling: TransferScheduling,
    /**
     * What this app expects each file to be. Indirected so the engine and the gates
     * can be exercised against a few kilobytes instead of 4 GB, and so there is one
     * source of truth for the expected size in both the download and the
     * post-download verification.
     */
    private val specFor: (ModelDownloadFile) -> RemoteFileSpec = { it.toSpec() },
    private val meteredProbe: () -> Boolean = { false },
    private val freeDiskProbe: () -> Long = { locator.freeDiskBytes() },
) {

    private val _uiState = MutableStateFlow(ModelDownloadUiState())
    val uiState: StateFlow<ModelDownloadUiState> = _uiState.asStateFlow()

    /** The coroutine running the transfer, so pause and cancel can stop it. */
    @Volatile
    private var activeTransfer: Job? = null

    @Volatile
    private var meteredConfirmed: Boolean = false

    // Exponential speed smoothing, ported from ModelDownloadService.swift.
    private var lastSampleAtMillis: Long = 0
    private var lastSampleBytes: Long = 0

    init {
        refreshPresence()
    }

    /**
     * Re-read the filesystem and reflect what is already on disk.
     *
     * Called on every visit to the setup screen, because files can arrive by routes
     * this class knows nothing about: the SAF importer, or an adb push.
     */
    fun refreshPresence() {
        val progress = ModelDownloadFile.entries.associateWith { file ->
            val size = locator.internalFileSize(file.fileName)
            FileDownloadProgress(
                bytesReceived = size,
                totalBytes = if (size > 0) size else file.approxBytes,
            )
        }
        val allPresent = ModelDownloadFile.entries.all { locator.isPresentInternally(it.fileName) }
        _uiState.update { current ->
            current.copy(
                progress = progress,
                state = if (allPresent && current.state !is ModelDownloadState.Downloading) {
                    ModelDownloadState.Done
                } else {
                    current.state
                },
                isMetered = runCatching { meteredProbe() }.getOrDefault(false),
            )
        }
    }

    /**
     * Preflight and schedule the transfer.
     *
     * Order matters and is the iOS order: already-done short-circuits, then disk,
     * then the metered gate. Asking the user to confirm a metered download that
     * would fail on disk space anyway is a worse experience than telling them about
     * the disk.
     */
    fun start(userConfirmedMetered: Boolean = false) {
        meteredConfirmed = meteredConfirmed || userConfirmedMetered
        _uiState.update { it.copy(needsMeteredConfirmation = false) }

        if (ModelDownloadFile.entries.all { locator.isPresentInternally(it.fileName) }) {
            _uiState.update { it.copy(state = ModelDownloadState.Done) }
            return
        }

        val free = runCatching { freeDiskProbe() }.getOrDefault(0L)
        if (free > 0 && !ModelDownloadLogic.hasEnoughDiskSpace(free)) {
            val neededGb = ModelDownloadLogic.REQUIRED_FREE_BYTES / 1_000_000_000.0
            dispatch(
                ModelDownloadLogic.Event.Failed(
                    "Not enough free space. Please free up about " +
                        "${String.format("%.1f", neededGb)} GB and try again."
                )
            )
            return
        }

        val metered = runCatching { meteredProbe() }.getOrDefault(false)
        _uiState.update { it.copy(isMetered = metered) }
        if (!ModelDownloadLogic.mayStartDownload(metered, meteredConfirmed)) {
            _uiState.update { it.copy(needsMeteredConfirmation = true) }
            return
        }

        dispatch(ModelDownloadLogic.Event.Start)
        scheduling.schedule(allowMetered = meteredConfirmed)
    }

    /** Dismiss the metered prompt without starting. */
    fun dismissMeteredConfirmation() {
        _uiState.update { it.copy(needsMeteredConfirmation = false) }
    }

    /**
     * Pause: stop the transfer and keep the partial.
     *
     * The state moves first so a cancellation-triggered failure cannot race ahead of
     * it and repaint the screen as an error.
     */
    fun pause() {
        if (_uiState.value.state !is ModelDownloadState.Downloading) return
        dispatch(ModelDownloadLogic.Event.Pause)
        resetSpeedSample()
        activeTransfer?.cancel()
        scheduling.cancel()
    }

    /** Resume a paused transfer. The `.part` files make this continue, not restart. */
    fun resume() {
        if (_uiState.value.state !is ModelDownloadState.Paused) return
        dispatch(ModelDownloadLogic.Event.Resume)
        scheduling.schedule(allowMetered = meteredConfirmed)
    }

    /**
     * Cancel everything and return to the not-started state.
     *
     * Files that already completed stay on disk, exactly as on iOS. Only the partial
     * in flight is discarded, because a partial with no job behind it is just
     * occupied space the user did not ask for.
     */
    fun cancel() {
        activeTransfer?.cancel()
        scheduling.cancel()
        resetSpeedSample()
        for (file in ModelDownloadFile.entries) {
            if (!locator.isPresentInternally(file.fileName)) {
                File(locator.internalDir(), file.fileName + ModelFileDownloader.PART_SUFFIX).delete()
            }
        }
        _uiState.update {
            it.copy(state = ModelDownloadState.NotStarted, currentFile = null, bytesPerSecond = 0.0)
        }
        refreshPresence()
    }

    /** Retry after a failure. */
    fun retry() {
        dispatch(ModelDownloadLogic.Event.Retry)
        scheduling.schedule(allowMetered = meteredConfirmed)
    }

    /**
     * Run the transfer. Called by the user-initiated data transfer job, or by the
     * pre-API-34 WorkManager fallback.
     *
     * @return true when every file is on disk and verified.
     */
    suspend fun runTransfer(): Boolean = coroutineScope {
        activeTransfer = coroutineContext[Job]
        try {
            if (_uiState.value.state !is ModelDownloadState.Downloading) {
                dispatch(ModelDownloadLogic.Event.Start)
            }
            // mmproj first, then weights. Enum order IS the download order.
            for (file in ModelDownloadFile.entries) {
                if (locator.isPresentInternally(file.fileName)) {
                    markComplete(file)
                    continue
                }
                _uiState.update { it.copy(currentFile = file) }
                resetSpeedSample()

                val outcome = downloader.download(specFor(file)) { received, total ->
                    publishProgress(file, received, total)
                }
                when (outcome) {
                    is DownloadOutcome.Success -> markComplete(file)
                    is DownloadOutcome.Failure -> {
                        dispatch(ModelDownloadLogic.Event.Failed(outcome.reason))
                        return@coroutineScope false
                    }
                }
            }
            dispatch(ModelDownloadLogic.Event.AllDownloaded)
            true
        } finally {
            activeTransfer = null
            _uiState.update { it.copy(bytesPerSecond = 0.0) }
        }
    }

    /**
     * The post-download check, run by WorkManager once the transfer job reports
     * success.
     *
     * Deliberately **not** a second full hash. The digest was computed streaming
     * while the bytes were written and checked before the atomic rename, so re-reading
     * 4 GB here would cost minutes to re-answer a question already answered. What is
     * worth re-asking is whether both halves of the pair are on disk at their exact
     * sizes and whether the locator actually resolves them, because that is what the
     * runtime will ask a moment later.
     */
    suspend fun verifyDownloadedFiles(): Boolean {
        // The verification worker can run in a process that did not do the transfer:
        // WorkManager will restart it after the app is killed. Walk the state machine
        // up to Verifying first, or every verdict below would land on a state that
        // does not accept it and be silently discarded as a no-op.
        if (_uiState.value.state !is ModelDownloadState.Verifying) {
            dispatch(ModelDownloadLogic.Event.Start)
            dispatch(ModelDownloadLogic.Event.AllDownloaded)
        }
        for (file in ModelDownloadFile.entries) {
            val actual = locator.internalFileSize(file.fileName)
            if (!ModelDownloadLogic.sizeIsValid(actual = actual, expected = specFor(file).exactBytes)) {
                dispatch(
                    ModelDownloadLogic.Event.VerificationFailed(
                        "${file.displayName} looked incomplete, so it was not accepted. " +
                            "Tap Retry to finish the download."
                    )
                )
                return false
            }
        }
        if (!locator.isModelPresent()) {
            dispatch(
                ModelDownloadLogic.Event.VerificationFailed(
                    "The downloaded files could not be opened. Tap Retry to download them again."
                )
            )
            return false
        }
        dispatch(ModelDownloadLogic.Event.VerifiedOK)
        refreshPresence()
        return true
    }

    /** Report a transfer that stopped for a reason the caller already has words for. */
    fun reportFailure(reason: String) {
        dispatch(ModelDownloadLogic.Event.Failed(reason))
    }

    /** True while a transfer should be running, so the job knows whether to keep going. */
    fun isActive(): Boolean = _uiState.value.state is ModelDownloadState.Downloading

    // MARK: - Internals

    private fun dispatch(event: ModelDownloadLogic.Event) {
        _uiState.update { it.copy(state = ModelDownloadLogic.next(it.state, event)) }
    }

    private fun markComplete(file: ModelDownloadFile) {
        val size = locator.internalFileSize(file.fileName).takeIf { it > 0 } ?: specFor(file).exactBytes
        _uiState.update { current ->
            current.copy(
                progress = current.progress + (file to FileDownloadProgress(size, size)),
            )
        }
    }

    private fun publishProgress(file: ModelDownloadFile, received: Long, total: Long) {
        _uiState.update { current ->
            current.copy(
                progress = current.progress + (
                    file to FileDownloadProgress(
                        bytesReceived = received,
                        totalBytes = if (total > 0) total else file.approxBytes,
                    )
                    ),
            )
        }
        updateSpeed(received)
    }

    /**
     * Exponentially smoothed bytes-per-second, ported from
     * `ModelDownloadService.swift`: sample no more than twice a second, then blend
     * 70 % of the old estimate with 30 % of the new one. Raw instantaneous rates on a
     * 64 KB buffer are far too jittery to put on screen.
     */
    private fun updateSpeed(received: Long) {
        val now = System.currentTimeMillis()
        if (lastSampleAtMillis == 0L) {
            lastSampleAtMillis = now
            lastSampleBytes = received
            return
        }
        val elapsedMillis = now - lastSampleAtMillis
        if (elapsedMillis < SPEED_SAMPLE_INTERVAL_MILLIS) return
        val instant = ((received - lastSampleBytes).toDouble() / elapsedMillis) * 1000.0
        val smoothed = if (_uiState.value.bytesPerSecond == 0.0) {
            maxOf(0.0, instant)
        } else {
            _uiState.value.bytesPerSecond * 0.7 + maxOf(0.0, instant) * 0.3
        }
        lastSampleAtMillis = now
        lastSampleBytes = received
        _uiState.update { it.copy(bytesPerSecond = smoothed) }
    }

    private fun resetSpeedSample() {
        lastSampleAtMillis = 0
        lastSampleBytes = 0
        _uiState.update { it.copy(bytesPerSecond = 0.0) }
    }

    companion object {
        private const val SPEED_SAMPLE_INTERVAL_MILLIS = 500L

        @Volatile
        private var instance: ModelDownloadRepository? = null

        /**
         * The process-wide instance.
         *
         * @param allowExternalModelsDir passed through to the locator. The downloader
         *   always writes into `filesDir`, so this only affects what the setup
         *   screen's picker can see.
         */
        fun get(context: Context, allowExternalModelsDir: Boolean = false): ModelDownloadRepository {
            instance?.let { return it }
            return synchronized(this) {
                instance ?: build(context.applicationContext, allowExternalModelsDir).also {
                    instance = it
                }
            }
        }

        /** Install a specific instance. For tests and for plan Step 47's container. */
        fun install(repository: ModelDownloadRepository?) {
            synchronized(this) { instance = repository }
        }

        private fun build(appContext: Context, allowExternalModelsDir: Boolean): ModelDownloadRepository {
            val locator = ModelFileLocator.create(appContext, allowExternalModelsDir)
            val modelsDir = locator.ensureInternalModelsDir()
            return ModelDownloadRepository(
                locator = locator,
                downloader = ModelFileDownloader(
                    client = ModelFileDownloader.defaultClient(),
                    modelsDir = modelsDir,
                ),
                scheduling = TransferScheduling.forContext(appContext),
                meteredProbe = {
                    val manager = appContext.getSystemService(Context.CONNECTIVITY_SERVICE)
                        as? ConnectivityManager
                    manager?.isActiveNetworkMetered ?: false
                },
            )
        }
    }
}
