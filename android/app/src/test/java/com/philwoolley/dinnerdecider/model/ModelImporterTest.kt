package com.philwoolley.dinnerdecider.model

import android.content.Context
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.io.File
import java.io.FileInputStream
import java.security.MessageDigest

/**
 * Host-JVM cover for the local import path.
 *
 * Exercises [ModelImporter.copyAndVerify], which is the whole of the import except
 * for asking the Storage Access Framework what the user picked. That split is what
 * lets the copy, the size gate, the digest gate, the atomic rename and the
 * trust-store bookkeeping all be proved without a document provider in the loop.
 * Robolectric is here only to supply a real `ContentResolver` and a real [Intent];
 * every file operation below is genuine I/O against a temporary folder.
 *
 * The property that matters throughout: an import that fails for any reason leaves
 * **nothing** under the real filename, because `ModelFileLocator` trusts
 * `filesDir/models` on location and would hand anything sitting there straight to
 * llama.cpp's native GGUF parser.
 */
@RunWith(RobolectricTestRunner::class)
class ModelImporterTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var modelsDir: File
    private lateinit var sourceDir: File

    private val weightsName = "gemma-4-E4B-it-finetuned-demo.gguf"

    @Before
    fun setUp() {
        modelsDir = temporaryFolder.newFolder("files", "models")
        sourceDir = temporaryFolder.newFolder("downloads")
    }

    private fun subject(trusted: TrustedDigests = TrustedDigests(modelsDir)) = ModelImporter(
        contentResolver = ApplicationProvider.getApplicationContext<Context>().contentResolver,
        modelsDir = modelsDir,
        trustedDigests = trusted,
        freeDiskProbe = { Long.MAX_VALUE },
    )

    private fun source(name: String, bytes: ByteArray): File =
        File(sourceDir, name).apply { writeBytes(bytes) }

    private suspend fun import(
        file: File,
        asName: String,
        trusted: TrustedDigests = TrustedDigests(modelsDir),
        onProgress: (Long, Long) -> Unit = { _, _ -> },
    ): ImportResult = subject(trusted).copyAndVerify(
        fileName = asName,
        declaredSize = file.length(),
        openSourceChannel = { FileInputStream(file).channel },
        onProgress = onProgress,
    )

    private fun sha256(bytes: ByteArray): String =
        MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

    private fun partFile(name: String) = File(modelsDir, name + ModelFileDownloader.PART_SUFFIX)

    private fun modelsDirEntries(): List<String> =
        modelsDir.listFiles()?.map { it.name }?.sorted().orEmpty()

    // MARK: - The happy path

    @Test
    fun copiesIntoFilesDirAndRenamesIntoPlace() = runBlocking {
        val bytes = ByteArray(9000) { (it % 251).toByte() }

        val result = import(source("picked.bin", bytes), weightsName)

        assertTrue("expected success, got $result", result is ImportResult.Success)
        val success = result as ImportResult.Success
        // Everything lands in filesDir/models and nowhere else: that is what makes the
        // imported file trustworthy to the locator afterwards.
        assertEquals(modelsDir.absolutePath, success.file.parentFile?.absolutePath)
        assertEquals(weightsName, success.file.name)
        assertArrayEquals(bytes, success.file.readBytes())
        assertFalse("the rename moved the part file, it was not copied", partFile(weightsName).exists())
        assertEquals(sha256(bytes), success.sha256)
        assertFalse("a fine-tuned build has no compiled-in digest to match", success.matchedPinnedDigest)
    }

    @Test
    fun anImportedFileIsImmediatelyResolvableByTheLocator() = runBlocking {
        import(source("w.bin", ByteArray(2048) { 7 }), weightsName)
        // A non-pinned projector name: the shipped Q8_0 name carries a compiled-in
        // exact size of 534 MB, which no unit test is going to produce.
        import(source("m.bin", ByteArray(1024) { 9 }), "mmproj-test-build.gguf")

        val locator = ModelFileLocator(
            internalModelsDir = modelsDir,
            externalModelsDir = null,
            allowExternalModelsDir = false,
            trustedDigests = TrustedDigests(modelsDir),
        )

        assertTrue(locator.isModelPresent())
        assertEquals(weightsName, locator.resolveWeights(preferred = null)?.name)
        assertEquals(ModelFileOrigin.INTERNAL, locator.resolveWeights(preferred = null)?.origin)
    }

    @Test
    fun theDigestIsRecordedSoAnExternalCopyOfTheSameFileCanLaterBeVerified() = runBlocking {
        val bytes = ByteArray(4096) { (it % 13).toByte() }

        import(source("w.bin", bytes), weightsName)

        assertEquals(sha256(bytes), TrustedDigests(modelsDir).digestFor(weightsName))
    }

    @Test
    fun anExistingFileIsReplaced() = runBlocking {
        File(modelsDir, weightsName).writeBytes(ByteArray(3))
        val bytes = ByteArray(5000) { 42 }

        val result = import(source("w.bin", bytes), weightsName)

        assertTrue(result is ImportResult.Success)
        assertArrayEquals(bytes, File(modelsDir, weightsName).readBytes())
    }

    @Test
    fun progressIsReportedUpToTheFullSize() = runBlocking {
        val bytes = ByteArray(7000) { 1 }
        val samples = mutableListOf<Pair<Long, Long>>()

        import(source("w.bin", bytes), weightsName) { copied, total -> samples += copied to total }

        assertTrue(samples.isNotEmpty())
        assertEquals(bytes.size.toLong(), samples.last().first)
        assertEquals(bytes.size.toLong(), samples.last().second)
    }

    @Test
    fun mmprojNamesAreAcceptedToo() = runBlocking {
        val result = import(source("m.bin", ByteArray(64) { 5 }), "MMPROJ-custom-build.GGUF")

        assertTrue("expected success, got $result", result is ImportResult.Success)
    }

    // MARK: - Name gates

    @Test
    fun aFileThatIsNotAGgufIsRejectedWithoutCopyingAnything() = runBlocking {
        val result = import(source("notes.txt", ByteArray(10)), "notes.txt")

        assertTrue(result is ImportResult.Rejected)
        assertTrue((result as ImportResult.Rejected).reason.contains(".gguf"))
        assertEquals(emptyList<String>(), modelsDirEntries())
    }

    @Test
    fun aGgufTheLocatorWouldNeverSelectIsRejectedRatherThanSilentlyCopied() = runBlocking {
        // Copying a file the selection logic can never choose would leave the user
        // staring at "no model" with gigabytes of storage gone.
        val result = import(source("x.bin", ByteArray(10)), "llama-3-8b-instruct.gguf")

        assertTrue(result is ImportResult.Rejected)
        assertTrue((result as ImportResult.Rejected).reason.contains(ModelFileSelection.MODEL_PREFIX))
        assertEquals(emptyList<String>(), modelsDirEntries())
    }

    @Test
    fun theCaseSensitivityAsymmetryOfTheContractIsHonoured() = runBlocking {
        // Prefix case-sensitive, suffix case-insensitive. The same rule the locator
        // applies, so an import can never produce a file selection would then skip.
        assertTrue(import(source("a.bin", ByteArray(8)), "gemma-4-E4B-it-x.GGUF") is ImportResult.Success)
        assertTrue(import(source("b.bin", ByteArray(8)), "gemma-4-e4b-it-x.gguf") is ImportResult.Rejected)
    }

    @Test
    fun aProviderSuppliedNameCannotEscapeTheModelsDirectory() = runBlocking {
        // A display name comes from a content provider, so it is attacker-influenced,
        // and it is concatenated onto filesDir/models.
        for (name in listOf(
            "../../../data/data/com.philwoolley.dinnerdecider/gemma-4-E4B-it-x.gguf",
            "sub/gemma-4-E4B-it-x.gguf",
            "sub\\gemma-4-E4B-it-x.gguf",
            "..",
            ".hidden-gemma-4-E4B-it-x.gguf",
            "",
        )) {
            val result = import(source("src.bin", ByteArray(8)), name)
            assertTrue("$name must be rejected, got $result", result is ImportResult.Rejected)
        }
        assertEquals(emptyList<String>(), modelsDirEntries())
    }

    @Test
    fun theSafeNameCheckIsDirectlyPinned() {
        assertTrue(ModelImporter.isSafeFileName("gemma-4-E4B-it-UD-IQ3_XXS.gguf"))
        assertFalse(ModelImporter.isSafeFileName("a/b.gguf"))
        assertFalse(ModelImporter.isSafeFileName("a\\b.gguf"))
        assertFalse(ModelImporter.isSafeFileName(".."))
        assertFalse(ModelImporter.isSafeFileName("."))
        assertFalse(ModelImporter.isSafeFileName(".hidden.gguf"))
        assertFalse(ModelImporter.isSafeFileName(""))
        assertFalse(ModelImporter.isSafeFileName("   "))
        assertFalse(ModelImporter.isSafeFileName("a".repeat(256)))
        assertFalse(ModelImporter.isSafeFileName("has space.gguf"))
    }

    // MARK: - Integrity gates

    @Test
    fun aPinnedFileWithTheWrongSizeIsRefusedAndNothingIsLeftBehind() = runBlocking {
        val trusted = TrustedDigests(
            modelsDir = modelsDir,
            pinned = mapOf(weightsName to sha256(ByteArray(100) { 1 })),
            pinnedSizes = mapOf(weightsName to 100L),
        )

        val result = import(source("w.bin", ByteArray(99) { 1 }), weightsName, trusted)

        assertTrue(result is ImportResult.Failed)
        assertTrue((result as ImportResult.Failed).reason.contains("size this app expects"))
        assertFalse(File(modelsDir, weightsName).exists())
        assertFalse(partFile(weightsName).exists())
    }

    @Test
    fun aPinnedFileWithTheRightSizeButTheWrongDigestIsRefusedAndNothingIsLeftBehind() = runBlocking {
        val expected = ByteArray(128) { 1 }
        val impostor = ByteArray(128) { 2 }
        val trusted = TrustedDigests(
            modelsDir = modelsDir,
            pinned = mapOf(weightsName to sha256(expected)),
            pinnedSizes = mapOf(weightsName to expected.size.toLong()),
        )

        val result = import(source("w.bin", impostor), weightsName, trusted)

        assertTrue("expected a failure, got $result", result is ImportResult.Failed)
        assertTrue((result as ImportResult.Failed).reason.contains("security check"))
        // Never renamed into place, so the locator cannot pick it up.
        assertFalse(File(modelsDir, weightsName).exists())
        assertFalse(partFile(weightsName).exists())
    }

    @Test
    fun aPinnedFileThatMatchesIsFlaggedAndDoesNotGetARecordedEntry() = runBlocking {
        val bytes = ByteArray(256) { 3 }
        val trusted = TrustedDigests(
            modelsDir = modelsDir,
            pinned = mapOf(weightsName to sha256(bytes)),
            pinnedSizes = mapOf(weightsName to bytes.size.toLong()),
        )

        val result = import(source("w.bin", bytes), weightsName, trusted)

        assertTrue(result is ImportResult.Success)
        assertTrue((result as ImportResult.Success).matchedPinnedDigest)
        // record() refuses to write an entry for a pinned name, so nothing can shadow it.
        assertFalse(File(modelsDir, TrustedDigests.RECORD_FILE_NAME).exists())
    }

    @Test
    fun anEmptySourceIsRefused() = runBlocking {
        val result = import(source("empty.bin", ByteArray(0)), weightsName)

        assertTrue(result is ImportResult.Failed)
        assertTrue((result as ImportResult.Failed).reason.contains("empty"))
        assertFalse(File(modelsDir, weightsName).exists())
    }

    // MARK: - The picker intent

    @Test
    fun thePickerAsksForADocumentWithAWildcardType() {
        val intent = ModelImporter.openDocumentIntent()

        // ACTION_OPEN_DOCUMENT, not ACTION_GET_CONTENT: the grant is scoped to the one
        // document the user chose. The type is the wildcard because GGUF has no
        // registered MIME type, and a narrower filter would hide the file on most
        // providers.
        assertEquals(Intent.ACTION_OPEN_DOCUMENT, intent.action)
        assertEquals("*/*", intent.type)
        assertTrue(intent.categories?.contains(Intent.CATEGORY_OPENABLE) == true)
    }
}
