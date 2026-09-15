package com.philwoolley.dinnerdecider.model

import com.philwoolley.dinnerdecider.contract.ModelDownloadFile
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

/**
 * Host-JVM cover for the Android model search order.
 *
 * Runs against real directories over a [TemporaryFolder], with no `android.*` in
 * sight, because [ModelFileLocator] takes its two directories as plain [File]s. The
 * `Context` convenience factory is the only part that touches the framework, and it
 * does nothing but name those two directories.
 *
 * The security property under test is the inverted search order: internal first,
 * external only behind the developer toggle and only after a digest check. Every
 * "external is refused" case here is a case that, on iOS's ordering, would have
 * handed an attacker-supplied binary to llama.cpp's native GGUF parser.
 */
class ModelFileLocatorTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var internalDir: File
    private lateinit var externalDir: File

    private val weightsName = ModelFileSelection.DEFAULT_MODEL_PREFERENCE[0]
    private val mmprojName = ModelFileSelection.PREFERRED_MMPROJ_NAME
    private val finetunedName = "gemma-4-E4B-it-finetuned-demo.gguf"

    @Before
    fun setUp() {
        internalDir = temporaryFolder.newFolder("files", "models")
        externalDir = temporaryFolder.newFolder("external", "models")
    }

    private fun locator(allowExternal: Boolean): ModelFileLocator = ModelFileLocator(
        internalModelsDir = internalDir,
        externalModelsDir = externalDir,
        allowExternalModelsDir = allowExternal,
        trustedDigests = TrustedDigests(internalDir),
    )

    private fun write(directory: File, name: String, content: String): File =
        File(directory, name).apply {
            parentFile?.mkdirs()
            writeText(content)
        }

    private fun sha256(content: String): String {
        val file = File(temporaryFolder.newFolder(), "probe.bin")
        file.writeText(content)
        return requireNotNull(ModelFileLocator.sha256Of(file))
    }

    // MARK: - Ordering

    @Test
    fun prefersTheInternalCopyWhenBothDirectoriesHaveTheFile() = runBlocking {
        write(internalDir, weightsName, "internal-bytes")
        write(externalDir, weightsName, "external-bytes")

        val resolved = locator(allowExternal = true).resolveWeights(preferred = null)

        assertNotNull(resolved)
        assertEquals(ModelFileOrigin.INTERNAL, resolved!!.origin)
        assertEquals(internalDir.absolutePath, resolved.file.parentFile?.absolutePath)
        assertEquals("internal-bytes", resolved.file.readText())
    }

    @Test
    fun internalCopyIsAcceptedOnLocationWithoutHashing() = runBlocking {
        // No trusted digest exists for this name and none is needed: filesDir is not
        // cross-app writable, so presence there is the verification.
        write(internalDir, finetunedName, "whatever the user imported")

        val resolved = locator(allowExternal = false).resolveWeights(preferred = null)

        assertNotNull(resolved)
        assertEquals(ModelFileOrigin.INTERNAL, resolved!!.origin)
        assertFalse(resolved.digestVerified)
    }

    @Test
    fun externalDirectoryIsInvisibleWhileTheDeveloperToggleIsOff() = runBlocking {
        val trusted = TrustedDigests(internalDir)
        val content = "perfectly-valid-external-copy"
        write(externalDir, finetunedName, content)
        trusted.record(finetunedName, sha256(content))

        val off = locator(allowExternal = false)

        // Not enumerated, not resolved, not hashed. The toggle gates the directory
        // itself, not merely whether its contents are trusted.
        assertEquals(emptyList<String>(), off.availableModelFileNames())
        assertNull(off.resolveWeights(preferred = null))
        assertFalse(off.externalSearchEnabled())
    }

    // MARK: - External verification

    @Test
    fun externalCopyIsAcceptedOnlyAfterItsDigestMatches() = runBlocking {
        val content = "the exact bytes this app imported earlier"
        write(externalDir, finetunedName, content)
        TrustedDigests(internalDir).record(finetunedName, sha256(content))

        val resolved = locator(allowExternal = true).resolveWeights(preferred = null)

        assertNotNull(resolved)
        assertEquals(ModelFileOrigin.EXTERNAL, resolved!!.origin)
        assertTrue(resolved.digestVerified)
    }

    @Test
    fun externalCopyWithAWrongDigestIsRefused() = runBlocking {
        TrustedDigests(internalDir).record(finetunedName, sha256("the file we vouched for"))
        // A hostile app swaps in its own GGUF under the same name.
        write(externalDir, finetunedName, "an attacker-chosen binary")

        assertNull(locator(allowExternal = true).resolveWeights(preferred = null))
    }

    @Test
    fun externalCopyWithNoTrustedDigestAtAllIsRefused() = runBlocking {
        write(externalDir, finetunedName, "never seen this file before")

        // "We have never vouched for a file by this name" is exactly the case the
        // check exists for, so it must be a refusal and not a pass.
        assertNull(locator(allowExternal = true).resolveWeights(preferred = null))
    }

    @Test
    fun pinnedFileWithTheWrongSizeIsRefusedBeforeAnyHashing() = runBlocking {
        // The pinned pair carries exact byte sizes. A 20-byte "weights file" cannot be
        // the 3.46 GB original, and rejecting on size means we never read 3.46 GB to
        // find that out.
        write(externalDir, weightsName, "far too small")

        assertNull(locator(allowExternal = true).resolveWeights(preferred = null))
    }

    @Test
    fun aFailingExternalCopyDoesNotMaskAGoodInternalOne() = runBlocking {
        write(internalDir, finetunedName, "the good copy")
        write(externalDir, finetunedName, "the bad copy")

        val resolved = locator(allowExternal = true).resolveWeights(preferred = null)

        assertNotNull(resolved)
        assertEquals("the good copy", resolved!!.file.readText())
    }

    @Test
    fun replacingAVerifiedExternalFileInvalidatesTheCachedVerdict() = runBlocking {
        val content = "the bytes we vouched for"
        val file = write(externalDir, finetunedName, content)
        TrustedDigests(internalDir).record(finetunedName, sha256(content))
        val subject = locator(allowExternal = true)

        assertNotNull(subject.resolveWeights(preferred = null))

        // Same locator instance, same path, different bytes. The cache key carries size
        // and last-modified precisely so this cannot ride on the earlier pass.
        file.writeText("swapped out from under us")
        file.setLastModified(file.lastModified() + 10_000L)

        assertNull(subject.resolveWeights(preferred = null))
    }

    // MARK: - Enumeration

    @Test
    fun enumerationAppliesTheContractsPrefixAndSuffixAsymmetry() {
        write(internalDir, weightsName, "x")
        write(internalDir, "gemma-4-E4B-it-Q3_K_S.GGUF", "x")
        write(internalDir, "gemma-4-e4b-it-lowercase.gguf", "x")
        write(internalDir, mmprojName, "x")
        write(internalDir, "notes.txt", "x")

        val names = locator(allowExternal = false).availableModelFileNames()

        assertTrue(weightsName in names)
        // Suffix is case-insensitive, so the .GGUF spelling is the same file.
        assertTrue("gemma-4-E4B-it-Q3_K_S.GGUF" in names)
        // Prefix is case-sensitive: E4B is part of the model's real name.
        assertFalse("gemma-4-e4b-it-lowercase.gguf" in names)
        // mmproj is excluded case-insensitively.
        assertFalse(mmprojName in names)
        assertFalse("notes.txt" in names)
    }

    @Test
    fun enumerationSpansBothDirectoriesInternalFirstAndDeduplicates() {
        write(internalDir, weightsName, "x")
        write(externalDir, weightsName, "x")
        write(externalDir, finetunedName, "x")

        val names = locator(allowExternal = true).availableModelFileNames()

        assertEquals(listOf(weightsName, finetunedName), names)
    }

    @Test
    fun mmprojEnumerationAndSelectionPreferTheQ8Build() = runBlocking {
        write(internalDir, "mmproj-some-other-build.gguf", "x")
        write(internalDir, mmprojName, "x")

        val subject = locator(allowExternal = false)

        assertEquals(setOf(mmprojName, "mmproj-some-other-build.gguf"), subject.availableMmprojFileNames().toSet())
        assertEquals(mmprojName, subject.resolveMmproj()?.name)
    }

    // MARK: - Pair presence

    @Test
    fun isModelPresentNeedsBothHalvesOfThePair() = runBlocking {
        val subject = locator(allowExternal = false)
        assertFalse(subject.isModelPresent())

        write(internalDir, weightsName, "x")
        assertFalse(subject.isModelPresent())

        write(internalDir, mmprojName, "x")
        assertTrue(subject.isModelPresent())
    }

    @Test
    fun userPinIsHonouredWhenPresentAndIgnoredWhenAbsent() = runBlocking {
        write(internalDir, weightsName, "x")
        write(internalDir, finetunedName, "x")
        val subject = locator(allowExternal = false)

        assertEquals(finetunedName, subject.resolveWeights(preferred = finetunedName)?.name)
        // A pin naming a file that is not there falls through to the built-in preference.
        assertEquals(weightsName, subject.resolveWeights(preferred = "gemma-4-E4B-it-absent.gguf")?.name)
    }

    // MARK: - Trust store

    @Test
    fun pinnedDigestsComeFromTheCompiledInPairAndCannotBeShadowed() {
        val trusted = TrustedDigests(internalDir)

        assertEquals(
            ModelDownloadFile.WEIGHTS.sha256.lowercase(),
            trusted.digestFor(ModelDownloadFile.WEIGHTS.fileName),
        )
        assertEquals(ModelDownloadFile.WEIGHTS.exactBytes, trusted.exactBytesFor(weightsName))

        // A recorded entry must never be able to redefine what the shipped weights are.
        trusted.record(weightsName, "00".repeat(32))
        assertEquals(
            ModelDownloadFile.WEIGHTS.sha256.lowercase(),
            trusted.digestFor(weightsName),
        )
    }

    @Test
    fun recordedDigestsSurviveANewStoreInstance() {
        TrustedDigests(internalDir).record(finetunedName, "AB".repeat(32))

        // Case-normalised on the way in, so a comparison never turns on hex casing.
        assertEquals("ab".repeat(32), TrustedDigests(internalDir).digestFor(finetunedName))
        assertNull(TrustedDigests(internalDir).exactBytesFor(finetunedName))
    }

    @Test
    fun theDigestRecordIsNotItselfEnumerableAsAModelFile() {
        TrustedDigests(internalDir).record(finetunedName, "ab".repeat(32))
        write(internalDir, finetunedName, "x")

        assertEquals(listOf(finetunedName), locator(allowExternal = false).availableModelFileNames())
        assertTrue(File(internalDir, TrustedDigests.RECORD_FILE_NAME).isFile)
    }

    @Test
    fun sha256MatchesTheDigestsRecordedInTheSharedContract() {
        // Not the 3.46 GB file, but the same streaming implementation the downloader,
        // the importer and this locator all share. A known vector pins it.
        val file = File(temporaryFolder.newFolder(), "abc.bin")
        file.writeText("abc")

        assertEquals(
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            ModelFileLocator.sha256Of(file),
        )
    }

    @Test
    fun emptyOrZeroLengthFilesAreNotResolved() = runBlocking {
        File(internalDir, weightsName).createNewFile()

        assertNull(locator(allowExternal = false).resolveWeights(preferred = null))
    }
}
