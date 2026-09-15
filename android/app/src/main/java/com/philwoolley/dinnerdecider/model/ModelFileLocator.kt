package com.philwoolley.dinnerdecider.model

import android.content.Context
import com.philwoolley.dinnerdecider.contract.ModelDownloadFile
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.security.MessageDigest

/**
 * Where model files may live on Android, and the names of those directories.
 *
 * [MODELS_DIR_NAME] is a subdirectory rather than the root of `filesDir` on purpose:
 * `filesDir` also holds the DataStore file and the Room database, and the backup
 * rules exclude `files/models` as a unit. A weights file dropped at the root of
 * `filesDir` would be swept into Auto Backup and pushed to the user's Drive.
 */
object ModelDirs {

    /** Subdirectory holding the GGUF weights and the vision projector. */
    const val MODELS_DIR_NAME: String = "models"

    /**
     * The verified, app-private model directory: `filesDir/models`.
     *
     * Not readable or writable by any other app on any supported API level. This is
     * the only directory [ModelFileLocator] trusts without a digest check.
     */
    fun internalModelsDir(context: Context): File =
        File(context.applicationContext.filesDir, MODELS_DIR_NAME)

    /**
     * The external app directory: `getExternalFilesDir("models")`.
     *
     * Returns null when no external volume is mounted. **Cross-app writable at
     * `minSdk 28`** (see [ModelFileLocator]), so nothing here is trusted on the
     * strength of its location.
     */
    fun externalModelsDir(context: Context): File? =
        context.applicationContext.getExternalFilesDir(MODELS_DIR_NAME)
}

/** Which directory a resolved file came from. */
enum class ModelFileOrigin {
    /** `filesDir/models`. App-private, so presence alone is sufficient. */
    INTERNAL,

    /** `getExternalFilesDir("models")`. Cross-app writable, so digest-checked. */
    EXTERNAL,
}

/**
 * A model file that is cleared to reach llama.cpp's native GGUF parser.
 *
 * [digestVerified] is true for every [ModelFileOrigin.EXTERNAL] result, because an
 * external file that failed verification is never returned at all. It is false for
 * an [ModelFileOrigin.INTERNAL] result that was accepted on location alone.
 */
data class ResolvedModelFile(
    val file: File,
    val origin: ModelFileOrigin,
    val digestVerified: Boolean,
) {
    val name: String get() = file.name
    val path: String get() = file.absolutePath
}

/**
 * The digests this app is willing to vouch for, keyed by filename.
 *
 * ### Why a store inside `filesDir` and not a sidecar next to the file
 * A trust anchor has to live somewhere the attacker cannot write. A
 * `weights.gguf.sha256` sidecar in the external app directory would be writable by
 * exactly the same app that just swapped the weights, so it proves nothing: the
 * attacker supplies both halves and they agree. `filesDir` is app-private on every
 * supported API level, which makes it the only place on the device where a digest
 * record means anything.
 *
 * The store is seeded from [ModelDownloadFile], whose digests are compiled into the
 * APK and measured in the working tree, and is appended to by the downloader and the
 * SAF importer as they verify files they wrote themselves.
 */
class TrustedDigests(private val modelsDir: File) {

    private val recordFile: File get() = File(modelsDir, RECORD_FILE_NAME)

    /**
     * The digest we expect for [fileName], or null when this app has never vouched
     * for a file by that name.
     *
     * Compiled-in digests win over recorded ones. A recorded entry can only ever be
     * written by this app, but the pinned pair is the stronger statement, and letting
     * a recorded entry shadow it would mean a bad import could redefine what the
     * shipped weights are allowed to be.
     */
    fun digestFor(fileName: String): String? {
        PINNED[fileName]?.let { return it }
        return readRecords()[fileName]
    }

    /** The exact byte size expected for [fileName], when it is one of the pinned pair. */
    fun exactBytesFor(fileName: String): Long? = PINNED_SIZES[fileName]

    /**
     * Record a digest for a file this app wrote and verified itself.
     *
     * Never call this for a file whose bytes came from an untrusted location: the
     * whole value of the record is that it was taken at a moment we controlled.
     */
    @Synchronized
    fun record(fileName: String, sha256: String) {
        if (fileName in PINNED) return
        val records = readRecords().toMutableMap()
        records[fileName] = sha256.lowercase()
        modelsDir.mkdirs()
        recordFile.writeText(
            records.entries.joinToString("\n") { "${it.key}=${it.value}" },
            Charsets.UTF_8,
        )
    }

    private fun readRecords(): Map<String, String> {
        if (!recordFile.isFile) return emptyMap()
        return runCatching {
            recordFile.readLines(Charsets.UTF_8)
                .mapNotNull { line ->
                    val index = line.indexOf('=')
                    if (index <= 0) return@mapNotNull null
                    line.substring(0, index) to line.substring(index + 1).trim().lowercase()
                }
                .toMap()
        }.getOrDefault(emptyMap())
    }

    companion object {
        /**
         * Name of the record file. The leading dot keeps it out of the GGUF
         * enumeration, which filters on the `.gguf` suffix anyway.
         */
        const val RECORD_FILE_NAME: String = ".digests"

        /** Digests compiled into the APK, from `shared/runtime/model-files.md`. */
        val PINNED: Map<String, String> =
            ModelDownloadFile.entries.associate { it.fileName to it.sha256.lowercase() }

        /** Exact byte sizes for the same pair, used as a cheap pre-check before hashing. */
        val PINNED_SIZES: Map<String, Long> =
            ModelDownloadFile.entries.associate { it.fileName to it.exactBytes }
    }
}

/**
 * Finds the on-device model files (GGUF weights plus vision projector) on Android.
 *
 * Wraps the pure [ModelFileSelection] with real directory enumeration, resolving the
 * `TODO(unresolved)` that `shared/runtime/model-files.md` leaves open for a non-Apple
 * port: "no search path is specified for a Linux or Android port".
 *
 * ## The search order is a deliberate divergence from iOS
 *
 * iOS searches **user-supplied locations first** (Documents, then the bundle) so a
 * file dropped in over Finder wins. That ordering is safe on iOS because the app's
 * Documents directory is not cross-app writable: only the user, over a cable or the
 * Files app, can put something there.
 *
 * On Android at `minSdk 28` that assumption does not hold.
 * `getExternalFilesDir("models")` sits on external storage, and at API 28 any app
 * holding `WRITE_EXTERNAL_STORAGE` can write into another app's external directory.
 * A hostile app could therefore shadow SHA-256-verified weights with an arbitrary
 * GGUF, and that file would go **straight into llama.cpp's native GGUF parser** - a
 * C++ parser reading an attacker-chosen binary format in this app's process.
 *
 * So the order is inverted, and the trust is explicit:
 *
 * 1. `filesDir/models` - app-private, written only by the downloader or the SAF
 *    importer, both of which verify before they rename into place. Preferred.
 * 2. `getExternalFilesDir("models")` - searched **only** when
 *    [allowExternalModelsDir] is set, and every candidate is **digest-checked at
 *    load time** against [TrustedDigests] before it is returned.
 *
 * **Anything resolved outside `filesDir` is digest-checked before it reaches the
 * native loader, no exceptions.** There is no "trusted because the developer put it
 * there" path: the developer toggle only decides whether the directory is looked at,
 * never whether its contents are checked.
 *
 * **Assets are never searched.** A 3.46 GB weights file cannot ship inside an APK,
 * and there is no bundled-demo build to serve the role iOS's `Bundle.main` plays.
 *
 * ## The developer path for the staged weights
 *
 * The staged pair lives in the repo's gitignored `models/` directory. To put it on a
 * device without a 4 GB download:
 *
 * ```
 * adb push models/gemma-4-E4B-it-UD-IQ3_XXS.gguf \
 *   /sdcard/Android/data/com.philwoolley.dinnerdecider/files/models/
 * adb push models/mmproj-gemma-4-E4B-it-Q8_0.gguf \
 *   /sdcard/Android/data/com.philwoolley.dinnerdecider/files/models/
 * ```
 *
 * Both files are then found only with the developer toggle on, and both are hashed
 * before use - their digests are two of the compiled-in [TrustedDigests.PINNED]
 * entries, so they verify without any further setup. A file that is not one of the
 * pinned pair, and that this app did not itself import, has no trusted digest and is
 * refused: bring it in through `ModelImporter` instead, which copies it into
 * `filesDir` and records what it hashed.
 *
 * ## Hashing cost
 *
 * Verifying 3.46 GB is tens of seconds of I/O, so it is not something to do on every
 * model load. Results are cached in memory against path, size and last-modified
 * time, which means a touched or replaced file is re-hashed and an untouched one is
 * hashed once per process. A size mismatch against a pinned size short-circuits
 * before any hashing happens at all.
 *
 * @param internalModelsDir `filesDir/models`, always trusted by location.
 * @param externalModelsDir `getExternalFilesDir("models")`, or null when unmounted.
 * @param allowExternalModelsDir the developer toggle. Off means the external
 *   directory is not enumerated or read at all.
 * @param trustedDigests the trust anchor store, which lives inside [internalModelsDir].
 */
class ModelFileLocator(
    private val internalModelsDir: File,
    private val externalModelsDir: File?,
    private val allowExternalModelsDir: Boolean,
    private val trustedDigests: TrustedDigests = TrustedDigests(internalModelsDir),
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) {

    /** In-memory verification cache. Key carries size and mtime so a swap invalidates it. */
    private val verificationCache = HashMap<String, Boolean>()

    /**
     * Directories to search, **internal first**. The external directory appears only
     * when the developer toggle is on and the volume is actually mounted.
     */
    private fun searchDirectories(): List<File> = buildList {
        add(internalModelsDir)
        val external = externalModelsDir
        if (allowExternalModelsDir && external != null) add(external)
    }

    /** Create [internalModelsDir] if it is missing, and return it. */
    fun ensureInternalModelsDir(): File = internalModelsDir.apply { mkdirs() }

    /** The internal directory, whether or not it exists yet. */
    fun internalDir(): File = internalModelsDir

    /** Whether the external directory is currently being searched. */
    fun externalSearchEnabled(): Boolean = allowExternalModelsDir && externalModelsDir != null

    /**
     * Every weights filename visible across the searched directories, in search order,
     * de-duplicated by name.
     *
     * **Enumeration only: names here are candidates, not cleared files.** The weights
     * picker needs a list before anything has been hashed, and hashing a 3.46 GB file
     * to populate a list would make the screen unusable. Resolution is where the
     * digest check happens.
     */
    fun availableModelFileNames(): List<String> {
        val names = LinkedHashSet<String>()
        for (directory in searchDirectories()) {
            for (name in listNames(directory)) {
                if (name.startsWith(ModelFileSelection.MODEL_PREFIX) &&
                    name.lowercase().endsWith(".gguf") &&
                    !name.lowercase().contains("mmproj")
                ) {
                    names.add(name)
                }
            }
        }
        return names.toList()
    }

    /** Every projector filename visible across the searched directories. Enumeration only. */
    fun availableMmprojFileNames(): List<String> {
        val names = LinkedHashSet<String>()
        for (directory in searchDirectories()) {
            for (name in listNames(directory)) {
                if (ModelFileSelection.isMmproj(name)) names.add(name)
            }
        }
        return names.toList()
    }

    /**
     * Resolve the weights file, honouring the user's pin.
     *
     * Returns null when nothing eligible is present, and **also** when the only copy
     * lives outside `filesDir` and fails verification. A file we cannot vouch for is
     * indistinguishable, from here, from a file that is not there.
     */
    suspend fun resolveWeights(preferred: String?): ResolvedModelFile? {
        val chosen = ModelFileSelection.chooseModelFile(availableModelFileNames(), preferred)
            ?: return null
        return resolveByName(chosen)
    }

    /** Resolve the vision projector, preferring the Q8_0 build. Same refusal rule as weights. */
    suspend fun resolveMmproj(): ResolvedModelFile? {
        val chosen = ModelFileSelection.chooseMmprojFile(availableMmprojFileNames()) ?: return null
        return resolveByName(chosen)
    }

    /**
     * Both files resolved and cleared means the runtime can load a vision-capable model.
     *
     * Either one missing means no vision-capable model, and the app falls back to
     * canned mock data with a visible badge rather than failing.
     */
    suspend fun isModelPresent(preferred: String? = null): Boolean =
        resolveWeights(preferred) != null && resolveMmproj() != null

    /**
     * Cheap presence probe for UI that must not block: does a file by this name exist
     * in the internal directory with a non-zero length.
     *
     * Deliberately does **not** consult the external directory, and deliberately does
     * not hash. It answers "has the download landed", not "is this safe to load".
     */
    fun isPresentInternally(fileName: String): Boolean {
        val file = File(internalModelsDir, fileName)
        return file.isFile && file.length() > 0
    }

    /** Byte length of an internal file, or 0 when it is absent. */
    fun internalFileSize(fileName: String): Long {
        val file = File(internalModelsDir, fileName)
        return if (file.isFile) file.length() else 0L
    }

    /** Free bytes on the volume holding the internal models directory. */
    fun freeDiskBytes(): Long {
        val probe = if (internalModelsDir.exists()) internalModelsDir else internalModelsDir.parentFile
        return probe?.usableSpace ?: 0L
    }

    private suspend fun resolveByName(fileName: String): ResolvedModelFile? {
        for (directory in searchDirectories()) {
            val candidate = File(directory, fileName)
            if (!candidate.isFile || candidate.length() <= 0L) continue

            val internal = directory == internalModelsDir
            if (internal) {
                return ResolvedModelFile(candidate, ModelFileOrigin.INTERNAL, digestVerified = false)
            }
            if (verifyExternal(candidate)) {
                return ResolvedModelFile(candidate, ModelFileOrigin.EXTERNAL, digestVerified = true)
            }
            // Fall through: a failing external copy must not mask a later directory,
            // and must never be returned.
        }
        return null
    }

    /**
     * Hash an external candidate and compare it against the trusted digest for its name.
     *
     * Order matters. The size check runs first because it is free and rejects the
     * overwhelmingly common failure (a truncated or entirely different file) without
     * reading 3.46 GB. No trusted digest at all is a refusal, not a pass: "we have
     * never vouched for a file by this name" is precisely the case the check exists
     * for.
     */
    private suspend fun verifyExternal(candidate: File): Boolean {
        val expected = trustedDigests.digestFor(candidate.name) ?: return false
        val expectedSize = trustedDigests.exactBytesFor(candidate.name)
        if (expectedSize != null && candidate.length() != expectedSize) return false

        val key = cacheKey(candidate)
        synchronized(verificationCache) { verificationCache[key] }?.let { return it }

        val actual = withContext(ioDispatcher) { sha256Of(candidate) }
        val ok = actual != null && actual.equals(expected, ignoreCase = true)
        synchronized(verificationCache) { verificationCache[key] = ok }
        return ok
    }

    private fun cacheKey(file: File): String =
        "${file.absolutePath}|${file.length()}|${file.lastModified()}"

    private fun listNames(directory: File): List<String> =
        directory.listFiles()?.filter { it.isFile }?.map { it.name }?.sorted().orEmpty()

    companion object {

        /**
         * Build a locator for a real [Context].
         *
         * @param allowExternalModelsDir the developer toggle. **Default off.** Nothing
         *   in the shipping user flow turns it on: the downloader and the importer both
         *   write into `filesDir`, so the external directory exists purely to let a
         *   developer side-load the staged weights over adb.
         */
        fun create(context: Context, allowExternalModelsDir: Boolean = false): ModelFileLocator {
            val internal = ModelDirs.internalModelsDir(context)
            return ModelFileLocator(
                internalModelsDir = internal,
                externalModelsDir = runCatching { ModelDirs.externalModelsDir(context) }.getOrNull(),
                allowExternalModelsDir = allowExternalModelsDir,
                trustedDigests = TrustedDigests(internal),
            )
        }

        /** Buffer size for streaming digests. 1 MiB keeps a 3.46 GB hash I/O bound. */
        private const val DIGEST_BUFFER_BYTES = 1 shl 20

        /**
         * Streaming SHA-256 of a file, lowercase hex, or null when it cannot be read.
         *
         * Shared with the downloader and the importer so all three verify the same way.
         */
        fun sha256Of(file: File): String? = runCatching {
            val digest = MessageDigest.getInstance("SHA-256")
            file.inputStream().use { stream ->
                val buffer = ByteArray(DIGEST_BUFFER_BYTES)
                while (true) {
                    val read = stream.read(buffer)
                    if (read <= 0) break
                    digest.update(buffer, 0, read)
                }
            }
            digest.digest().toHexString()
        }.getOrNull()

        /** Lowercase hex, the form every digest in `shared/runtime/model-files.md` uses. */
        fun ByteArray.toHexString(): String =
            joinToString("") { byte -> "%02x".format(byte) }
    }
}
