package com.philwoolley.dinnerdecider.model

import com.philwoolley.dinnerdecider.contract.ModelDownloadFile
import com.philwoolley.dinnerdecider.contract.ModelDownloadLogic
import com.philwoolley.dinnerdecider.contract.ModelDownloadState
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * Host-JVM cover for the download orchestration: sequencing, gating and the
 * lifecycle state.
 *
 * Every gate here is [ModelDownloadLogic]'s, not the repository's, and that is the
 * point of the split. The repository holds no decision logic of its own, which is
 * what lets all of this be exercised with no JobScheduler, no WorkManager and no
 * `Context` at all: [TransferScheduling] is an interface, and both probes are
 * injected.
 */
class ModelDownloadRepositoryTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var server: MockWebServer
    private lateinit var modelsDir: File
    private lateinit var locator: ModelFileLocator

    private val mmprojBody = ByteArray(512) { (it % 97).toByte() }
    private val weightsBody = ByteArray(2048) { (it % 89).toByte() }

    private val scheduleCalls = mutableListOf<Boolean>()
    private var cancelCalls = 0
    private var metered = false
    private var freeDisk = ModelDownloadLogic.REQUIRED_FREE_BYTES

    private val scheduling = object : TransferScheduling {
        override fun schedule(allowMetered: Boolean) {
            scheduleCalls += allowMetered
        }

        override fun cancel() {
            cancelCalls += 1
        }
    }

    @Before
    fun setUp() {
        server = MockWebServer().apply { start() }
        modelsDir = temporaryFolder.newFolder("files", "models")
        locator = ModelFileLocator(
            internalModelsDir = modelsDir,
            externalModelsDir = null,
            allowExternalModelsDir = false,
            trustedDigests = TrustedDigests(modelsDir),
        )
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val body = if (request.path?.contains("mmproj") == true) mmprojBody else weightsBody
                return MockResponse().setResponseCode(200).setBody(Buffer().write(body))
            }
        }
    }

    @After
    fun tearDown() {
        server.shutdown()
        ModelDownloadRepository.install(null)
    }

    private fun bodyFor(file: ModelDownloadFile) =
        if (file == ModelDownloadFile.MMPROJ) mmprojBody else weightsBody

    private fun specFor(file: ModelDownloadFile): RemoteFileSpec {
        val body = bodyFor(file)
        val path = if (file == ModelDownloadFile.MMPROJ) "/mmproj.gguf" else "/weights.gguf"
        return RemoteFileSpec(
            fileName = file.fileName,
            url = server.url(path).toString(),
            exactBytes = body.size.toLong(),
            sha256 = MessageDigest.getInstance("SHA-256").digest(body)
                .joinToString("") { "%02x".format(it) },
            displayName = file.displayName,
        )
    }

    private fun repository(): ModelDownloadRepository = ModelDownloadRepository(
        locator = locator,
        downloader = ModelFileDownloader(
            client = OkHttpClient.Builder()
                .callTimeout(0, TimeUnit.MILLISECONDS)
                .readTimeout(5, TimeUnit.SECONDS)
                .retryOnConnectionFailure(true)
                .build(),
            modelsDir = modelsDir,
            trustedDigests = TrustedDigests(modelsDir),
        ),
        scheduling = scheduling,
        specFor = ::specFor,
        meteredProbe = { metered },
        freeDiskProbe = { freeDisk },
    )

    // MARK: - Sequencing

    @Test
    fun theProjectorDownloadsBeforeTheWeights() = runBlocking {
        val subject = repository()

        assertTrue(subject.runTransfer())

        // 534 MB before 3.46 GB is deliberate: the small file gives a fast,
        // encouraging first success. Enum order IS download order, so reordering the
        // enum would silently undo it.
        assertEquals("/mmproj.gguf", server.takeRequest().path)
        assertEquals("/weights.gguf", server.takeRequest().path)
        assertTrue(File(modelsDir, ModelDownloadFile.MMPROJ.fileName).isFile)
        assertTrue(File(modelsDir, ModelDownloadFile.WEIGHTS.fileName).isFile)
    }

    @Test
    fun aCompletedTransferMovesToVerifyingThenDone() = runBlocking {
        val subject = repository()

        subject.runTransfer()
        assertEquals(ModelDownloadState.Verifying, subject.uiState.value.state)

        assertTrue(subject.verifyDownloadedFiles())
        assertEquals(ModelDownloadState.Done, subject.uiState.value.state)
    }

    @Test
    fun aFileAlreadyOnDiskIsNotDownloadedAgain() = runBlocking {
        File(modelsDir, ModelDownloadFile.MMPROJ.fileName).writeBytes(mmprojBody)
        val subject = repository()

        assertTrue(subject.runTransfer())

        assertEquals(1, server.requestCount)
        assertEquals("/weights.gguf", server.takeRequest().path)
    }

    @Test
    fun aFailedFileStopsTheSequenceAndCarriesItsReason() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(404)
        }
        val subject = repository()

        assertFalse(subject.runTransfer())

        val state = subject.uiState.value.state
        assertTrue(state is ModelDownloadState.Failed)
        assertTrue((state as ModelDownloadState.Failed).reason.isNotBlank())
        // The weights request is never made: a failure stops the sequence.
        assertEquals(1, server.requestCount)
    }

    // MARK: - Gates, all of them the contract's

    @Test
    fun aMeteredConnectionNeedsConfirmationBeforeAnythingIsScheduled() {
        metered = true
        val subject = repository()

        subject.start()

        assertTrue(subject.uiState.value.needsMeteredConfirmation)
        assertEquals(ModelDownloadState.NotStarted, subject.uiState.value.state)
        assertTrue("nothing may be scheduled before the user confirms", scheduleCalls.isEmpty())

        subject.start(userConfirmedMetered = true)

        assertFalse(subject.uiState.value.needsMeteredConfirmation)
        assertEquals(ModelDownloadState.Downloading, subject.uiState.value.state)
        assertEquals(listOf(true), scheduleCalls)
    }

    @Test
    fun theMeteredConfirmationStaysGivenForLaterStarts() {
        metered = true
        val subject = repository()
        subject.start(userConfirmedMetered = true)
        scheduleCalls.clear()

        subject.retry()

        // Asking again on every retry of a 4 GB download would be nagging, not consent.
        assertEquals(listOf(true), scheduleCalls)
    }

    @Test
    fun tooLittleDiskSpaceFailsWithAnActionableNumberAndSchedulesNothing() {
        freeDisk = ModelDownloadLogic.REQUIRED_FREE_BYTES - 1
        val subject = repository()

        subject.start()

        val state = subject.uiState.value.state
        assertTrue(state is ModelDownloadState.Failed)
        assertTrue((state as ModelDownloadState.Failed).reason.contains("4.5 GB"))
        assertTrue(scheduleCalls.isEmpty())
    }

    @Test
    fun theDiskGateIsInclusiveAtItsBoundary() {
        freeDisk = ModelDownloadLogic.REQUIRED_FREE_BYTES
        val subject = repository()

        subject.start()

        assertEquals(ModelDownloadState.Downloading, subject.uiState.value.state)
    }

    @Test
    fun theDiskGateIsCheckedBeforeTheMeteredGate() {
        // Asking the user to approve a metered download that would fail on disk space
        // anyway is a worse experience than telling them about the disk.
        freeDisk = 0L + 1
        metered = true
        val subject = repository()

        subject.start()

        assertTrue(subject.uiState.value.state is ModelDownloadState.Failed)
        assertFalse(subject.uiState.value.needsMeteredConfirmation)
    }

    @Test
    fun startIsAShortCircuitWhenBothFilesAreAlreadyThere() {
        File(modelsDir, ModelDownloadFile.MMPROJ.fileName).writeBytes(mmprojBody)
        File(modelsDir, ModelDownloadFile.WEIGHTS.fileName).writeBytes(weightsBody)
        val subject = repository()

        subject.start()

        assertEquals(ModelDownloadState.Done, subject.uiState.value.state)
        assertTrue(scheduleCalls.isEmpty())
    }

    // MARK: - Lifecycle

    @Test
    fun pauseAndResumeMoveThroughTheContractsStateTable() {
        val subject = repository()
        subject.start()
        assertEquals(ModelDownloadState.Downloading, subject.uiState.value.state)

        subject.pause()
        assertEquals(ModelDownloadState.Paused, subject.uiState.value.state)
        assertEquals(1, cancelCalls)

        subject.resume()
        assertEquals(ModelDownloadState.Downloading, subject.uiState.value.state)
        assertEquals(2, scheduleCalls.size)
    }

    @Test
    fun pauseIsIgnoredWhenNothingIsDownloading() {
        val subject = repository()

        subject.pause()

        // A stray pause on a finished or not-started transfer must be a no-op, not a
        // paused bar over a download that is not happening.
        assertEquals(ModelDownloadState.NotStarted, subject.uiState.value.state)
        assertEquals(0, cancelCalls)
    }

    @Test
    fun cancelKeepsFinishedFilesAndDropsOnlyThePartial() {
        File(modelsDir, ModelDownloadFile.MMPROJ.fileName).writeBytes(mmprojBody)
        val partial = File(
            modelsDir,
            ModelDownloadFile.WEIGHTS.fileName + ModelFileDownloader.PART_SUFFIX,
        ).apply { writeBytes(ByteArray(64)) }
        val subject = repository()
        subject.start()

        subject.cancel()

        assertEquals(ModelDownloadState.NotStarted, subject.uiState.value.state)
        assertTrue("a completed file is the user's, not ours to delete", File(modelsDir, ModelDownloadFile.MMPROJ.fileName).isFile)
        assertFalse("a partial with no job behind it is just occupied space", partial.exists())
    }

    @Test
    fun retryAfterAFailureReturnsToDownloading() = runBlocking {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse().setResponseCode(503)
        }
        val subject = repository()
        subject.runTransfer()
        assertTrue(subject.uiState.value.state is ModelDownloadState.Failed)

        subject.retry()

        assertEquals(ModelDownloadState.Downloading, subject.uiState.value.state)
    }

    // MARK: - Verification and presence

    @Test
    fun verificationRefusesAFileThatIsNotItsExactSize() = runBlocking {
        File(modelsDir, ModelDownloadFile.MMPROJ.fileName).writeBytes(mmprojBody)
        File(modelsDir, ModelDownloadFile.WEIGHTS.fileName).writeBytes(ByteArray(10))
        val subject = repository()

        assertFalse(subject.verifyDownloadedFiles())

        val state = subject.uiState.value.state
        assertTrue(state is ModelDownloadState.Failed)
        assertTrue((state as ModelDownloadState.Failed).reason.contains("incomplete"))
    }

    @Test
    fun refreshPresenceNoticesFilesThatArrivedByAnotherRoute() {
        val subject = repository()
        assertEquals(ModelDownloadState.NotStarted, subject.uiState.value.state)

        // An adb push or the SAF importer. The repository knows nothing about either.
        File(modelsDir, ModelDownloadFile.MMPROJ.fileName).writeBytes(mmprojBody)
        File(modelsDir, ModelDownloadFile.WEIGHTS.fileName).writeBytes(weightsBody)
        subject.refreshPresence()

        assertEquals(ModelDownloadState.Done, subject.uiState.value.state)
        assertEquals(1.0, subject.uiState.value.overallFraction, 1e-9)
    }

    @Test
    fun overallProgressIsTheContractsFractionAndNeverNaN() {
        val subject = repository()

        // Nothing downloaded yet: approximate totals stand in, and the fraction is 0.
        assertEquals(0.0, subject.uiState.value.overallFraction, 1e-9)
        assertEquals(
            ModelDownloadFile.entries.sumOf { it.approxBytes },
            subject.uiState.value.overall.totalBytes,
        )
    }

    @Test
    fun progressIsPublishedPerFileWhileTheTransferRuns() = runBlocking {
        val subject = repository()

        subject.runTransfer()

        val progress = subject.uiState.value.progress
        assertEquals(mmprojBody.size.toLong(), progress[ModelDownloadFile.MMPROJ]?.bytesReceived)
        assertEquals(weightsBody.size.toLong(), progress[ModelDownloadFile.WEIGHTS]?.bytesReceived)
        assertEquals(1.0, progress[ModelDownloadFile.WEIGHTS]?.fraction ?: 0.0, 1e-9)
    }
}
