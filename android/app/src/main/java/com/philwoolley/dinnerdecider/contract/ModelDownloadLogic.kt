package com.philwoolley.dinnerdecider.contract

/**
 * The two on-device model files, **in download order**.
 *
 * Port of `ModelDownloadService.swift:9-50`; sizes and digests from
 * `shared/runtime/model-files.md`. The small vision projector downloads first so
 * the user sees a fast, encouraging first success before the 3.5 GB weights start.
 */
enum class ModelDownloadFile(
    /**
     * Destination filename. Must match what [ModelFileSelection] resolves, or the
     * runtime will not pick the files up - pinned by a test.
     */
    val fileName: String,
    /** Public, no-auth direct download URL on Hugging Face. */
    val remoteUrl: String,
    /**
     * Approximate size, for sensible progress before the HEAD request returns the
     * exact `Content-Length`, and as a friendly label. **Deliberately approximate:
     * never use it as a verification constant** - that is [exactBytes].
     */
    val approxBytes: Long,
    /**
     * The exact byte count of the staged file, measured in the working tree.
     * Checked alongside [sha256] before a downloaded file is accepted.
     */
    val exactBytes: Long,
    /**
     * `shasum -a 256` of the staged file.
     *
     * Verifying the digest **and** the exact size is stricter than iOS, which
     * checked `Content-Length` only. That is a deliberate divergence in the safe
     * direction: this file is fed straight to llama.cpp's native GGUF parser, so
     * "we cannot vouch for these bytes" must mean "we do not load them".
     */
    val sha256: String,
    /** Short, human name for the UI and TalkBack. */
    val displayName: String,
) {
    MMPROJ(
        fileName = ModelFileSelection.PREFERRED_MMPROJ_NAME,
        remoteUrl = "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/" +
            "mmproj-gemma-4-E4B-it-Q8_0.gguf",
        approxBytes = 534_000_000L,
        exactBytes = 559_874_816L,
        sha256 = "197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1",
        displayName = "Vision projector",
    ),
    WEIGHTS(
        fileName = ModelFileSelection.DEFAULT_MODEL_PREFERENCE[0],
        remoteUrl = "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/" +
            "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
        approxBytes = 3_460_000_000L,
        exactBytes = 3_717_115_008L,
        sha256 = "dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7",
        displayName = "Gemma 4 weights",
    ),
}

/**
 * The download lifecycle. [Failed] carries a plain-language reason for the UI.
 *
 * Port of `ModelDownloadService.swift:55-63`.
 */
sealed interface ModelDownloadState {
    data object NotStarted : ModelDownloadState
    data object Downloading : ModelDownloadState
    data object Paused : ModelDownloadState
    data object Verifying : ModelDownloadState
    data object Done : ModelDownloadState
    data class Failed(val reason: String) : ModelDownloadState
}

/** Byte progress for a single file. Port of `ModelDownloadService.swift:66-74`. */
data class FileDownloadProgress(
    val bytesReceived: Long = 0,
    val totalBytes: Long = 0,
) {
    /** Zero when the total is unknown; capped at 1.0. */
    val fraction: Double
        get() = if (totalBytes > 0) minOf(1.0, bytesReceived.toDouble() / totalBytes) else 0.0
}

/**
 * Side-effect-free decision helpers for the model download.
 *
 * Port of `ModelDownloadService.swift:79-149`. Kept separate from the transfer
 * itself so every gate and the whole state machine are unit-testable without
 * touching the network, a job scheduler, or the filesystem.
 */
object ModelDownloadLogic {

    /**
     * Free space insisted on before starting: **4.5 GB**.
     *
     * Weights plus projector are about 4.0 GB; the extra 0.5 GB is headroom for the
     * temporary copy during the atomic move into place.
     */
    const val REQUIRED_FREE_BYTES: Long = 4_500_000_000L

    /** Whether there is enough free disk to safely download. Boundary inclusive. */
    fun hasEnoughDiskSpace(
        freeBytes: Long,
        required: Long = REQUIRED_FREE_BYTES,
    ): Boolean = freeBytes >= required

    /**
     * Whether a finished file's real size matches the expected size.
     *
     * A zero (unknown) expected size is treated as **invalid**: never accept a file
     * you cannot vouch for. That is the whole point of the check - a server that
     * omits `Content-Length` must not be able to talk the app into loading an
     * arbitrary blob as GGUF.
     */
    fun sizeIsValid(actual: Long, expected: Long): Boolean = expected > 0 && actual == expected

    /**
     * Whether a download may begin given the network.
     *
     * On a metered connection an explicit user confirmation is required first  - 
     * this is a multi-gigabyte transfer. (iOS names this `isCellular`; `isMetered`
     * is the Android framing, and covers metered Wi-Fi too.)
     */
    fun mayStartDownload(isMetered: Boolean, userConfirmedMetered: Boolean): Boolean =
        !(isMetered && !userConfirmedMetered)

    /** Overall completion fraction from summed bytes. Zero total is zero, not NaN. */
    fun overallFraction(received: Long, total: Long): Double =
        if (total > 0) minOf(1.0, received.toDouble() / total) else 0.0

    /** Events that drive the state machine. */
    sealed interface Event {
        data object Start : Event
        data object Pause : Event
        data object Resume : Event
        data object AllDownloaded : Event
        data object VerifiedOK : Event
        data class VerificationFailed(val reason: String) : Event
        data class Failed(val reason: String) : Event
        data object Retry : Event
    }

    /**
     * Pure state transition.
     *
     * **Any unhandled pairing is a no-op**, returning the current state. That is not
     * laziness: a transfer this long generates stray callbacks - a resume delivered
     * after completion, a pause arriving mid-verification - and treating them as
     * errors, or letting them fall through to some "reset" branch, is how the UI
     * ends up showing a paused bar for a finished download.
     *
     * [Event.Failed] is accepted from **every** state, by design.
     */
    fun next(state: ModelDownloadState, event: Event): ModelDownloadState = when {
        event is Event.Failed -> ModelDownloadState.Failed(event.reason)

        state is ModelDownloadState.NotStarted && event is Event.Start ->
            ModelDownloadState.Downloading
        state is ModelDownloadState.Failed && event is Event.Retry ->
            ModelDownloadState.Downloading
        state is ModelDownloadState.Failed && event is Event.Start ->
            ModelDownloadState.Downloading
        state is ModelDownloadState.Done && event is Event.Start ->
            ModelDownloadState.Downloading

        state is ModelDownloadState.Downloading && event is Event.Pause ->
            ModelDownloadState.Paused
        state is ModelDownloadState.Paused && event is Event.Resume ->
            ModelDownloadState.Downloading
        state is ModelDownloadState.Paused && event is Event.Start ->
            ModelDownloadState.Downloading

        state is ModelDownloadState.Downloading && event is Event.AllDownloaded ->
            ModelDownloadState.Verifying
        state is ModelDownloadState.Verifying && event is Event.VerifiedOK ->
            ModelDownloadState.Done
        state is ModelDownloadState.Verifying && event is Event.VerificationFailed ->
            ModelDownloadState.Failed(event.reason)

        else -> state
    }
}
