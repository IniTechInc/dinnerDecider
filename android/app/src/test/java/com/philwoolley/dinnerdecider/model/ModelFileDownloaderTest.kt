package com.philwoolley.dinnerdecider.model

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.mockwebserver.SocketPolicy
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * MockWebServer cover for the resumable, integrity-checked transfer engine.
 *
 * The four cases the plan calls out by name all live here: resume after a kill,
 * a truncated body, a wrong digest, and a server that ignores the range. Each one is
 * a way the shipped iOS check (`Content-Length` only) would have accepted bytes this
 * engine refuses, and each refusal keeps an unverified binary out of llama.cpp's
 * native GGUF parser.
 *
 * Runs on the host JVM against a real socket and a real filesystem. No Android
 * framework, no Robolectric.
 */
class ModelFileDownloaderTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var server: MockWebServer
    private lateinit var modelsDir: File
    private lateinit var client: OkHttpClient

    /** 4 KiB of deterministic, non-repeating bytes. Small enough to be instant, big enough to split. */
    private val payload: ByteArray = ByteArray(4096) { (it * 31 % 251).toByte() }
    private val payloadDigest: String = sha256Hex(payload)

    @Before
    fun setUp() {
        server = MockWebServer().apply { start() }
        modelsDir = temporaryFolder.newFolder("models")
        client = OkHttpClient.Builder()
            .callTimeout(0, TimeUnit.MILLISECONDS)
            .connectTimeout(5, TimeUnit.SECONDS)
            .readTimeout(5, TimeUnit.SECONDS)
            .retryOnConnectionFailure(true)
            .build()
    }

    @After
    fun tearDown() {
        server.shutdown()
    }

    private fun downloader(): ModelFileDownloader = ModelFileDownloader(
        client = client,
        modelsDir = modelsDir,
        trustedDigests = TrustedDigests(modelsDir),
    )

    private fun spec(
        exactBytes: Long = payload.size.toLong(),
        sha256: String = payloadDigest,
        fileName: String = "gemma-4-E4B-it-test.gguf",
    ) = RemoteFileSpec(
        fileName = fileName,
        url = server.url("/weights").toString(),
        exactBytes = exactBytes,
        sha256 = sha256,
        displayName = "Test weights",
    )

    private fun sha256Hex(bytes: ByteArray): String =
        MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

    private fun bodyOf(bytes: ByteArray): Buffer = Buffer().write(bytes)

    private fun partFile(spec: RemoteFileSpec) =
        File(modelsDir, spec.fileName + ModelFileDownloader.PART_SUFFIX)

    private fun destinationFile(spec: RemoteFileSpec) = File(modelsDir, spec.fileName)

    // MARK: - Happy path

    @Test
    fun downloadsVerifiesAndRenamesIntoPlace() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))
        val spec = spec()

        val outcome = downloader().download(spec)

        assertTrue("expected success, got $outcome", outcome is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())
        // The .part file is gone: the rename moved it, it was not copied.
        assertFalse(partFile(spec).exists())
        // No Range header on a fresh download.
        assertNull(server.takeRequest().getHeader("Range"))
    }

    @Test
    fun aFreshDownloadReportsProgressUpToTheFullSize() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))
        val samples = mutableListOf<Pair<Long, Long>>()

        downloader().download(spec()) { received, total -> samples += received to total }

        assertTrue(samples.isNotEmpty())
        assertEquals(payload.size.toLong(), samples.last().first)
        assertEquals(payload.size.toLong(), samples.last().second)
    }

    // MARK: - Resume after a kill

    @Test
    fun resumesAfterTheConnectionDiesMidBody() = runBlocking {
        val half = payload.size / 2
        // Claims the full length, delivers half, then drops the socket. This is what a
        // killed transfer looks like from the client side.
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setBody(bodyOf(payload.copyOfRange(0, half)))
                .setHeader("Content-Length", payload.size)
                .setSocketPolicy(SocketPolicy.DISCONNECT_AT_END)
        )
        val spec = spec()

        val first = downloader().download(spec)

        assertTrue("expected a failure, got $first", first is DownloadOutcome.Failure)
        val failure = first as DownloadOutcome.Failure
        assertTrue("a dropped connection is retryable", failure.retryable)
        assertTrue("the partial must survive so the retry resumes", failure.partialKept)
        assertEquals(half.toLong(), partFile(spec).length())
        assertFalse(destinationFile(spec).exists())

        // The retry continues from where it stopped and the digest still covers the
        // whole file, because the existing partial is fed through it first.
        server.enqueue(
            MockResponse()
                .setResponseCode(206)
                .setBody(bodyOf(payload.copyOfRange(half, payload.size)))
                .setHeader("Content-Range", "bytes $half-${payload.size - 1}/${payload.size}")
        )

        val second = downloader().download(spec)

        assertTrue("expected success, got $second", second is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())

        server.takeRequest()
        assertEquals("bytes=$half-", server.takeRequest().getHeader("Range"))
    }

    @Test
    fun aResumeThatStartsAtTheWrongOffsetRestartsCleanly() = runBlocking {
        val spec = spec()
        partFile(spec).writeBytes(payload.copyOfRange(0, 1000))

        // A 206 that begins somewhere other than where we asked would leave a hole in
        // the file. Restart rather than write one.
        server.enqueue(
            MockResponse()
                .setResponseCode(206)
                .setBody(bodyOf(payload.copyOfRange(2000, payload.size)))
                .setHeader("Content-Range", "bytes 2000-${payload.size - 1}/${payload.size}")
        )
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))

        val outcome = downloader().download(spec)

        assertTrue("expected success, got $outcome", outcome is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())
        assertEquals("bytes=1000-", server.takeRequest().getHeader("Range"))
        assertNull("the restart must not carry a Range header", server.takeRequest().getHeader("Range"))
    }

    // MARK: - A server that ignores the range

    @Test
    fun aServerThatIgnoresTheRangeCausesACleanRestartNotACorruptFile() = runBlocking {
        val spec = spec()
        val half = payload.size / 2
        partFile(spec).writeBytes(payload.copyOfRange(0, half))

        // Answering a Range request with 200 and the whole body is the dangerous case:
        // appending it would produce a file of plausible length made of the wrong
        // bytes, which a Content-Length check cannot see.
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse =
                MockResponse().setResponseCode(200).setBody(bodyOf(payload))
        }

        val outcome = downloader().download(spec)

        assertTrue("expected success, got $outcome", outcome is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())
        assertEquals(payload.size.toLong(), destinationFile(spec).length())

        // Two requests: the ranged one that was ignored, then a clean restart.
        assertEquals("bytes=$half-", server.takeRequest().getHeader("Range"))
        assertNull(server.takeRequest().getHeader("Range"))
        assertEquals(2, server.requestCount)
    }

    @Test
    fun aServerThatIgnoresTheRangeTwiceIsReportedRatherThanLoopedOn() = runBlocking {
        val spec = spec()
        partFile(spec).writeBytes(payload.copyOfRange(0, 100))

        // Always 200, and always shorter than expected, so the restart also produces a
        // partial and the engine would otherwise spin.
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = MockResponse()
                .setResponseCode(200)
                .setBody(bodyOf(payload.copyOfRange(0, 100)))
                .setHeader("Content-Length", payload.size)
                .setSocketPolicy(SocketPolicy.DISCONNECT_AT_END)
        }

        val outcome = downloader().download(spec)

        assertTrue(outcome is DownloadOutcome.Failure)
        // Bounded: one ranged attempt, one restart, then a report. Never an open loop.
        assertTrue("expected at most 2 requests, saw ${server.requestCount}", server.requestCount <= 2)
    }

    @Test
    fun a416RestartsFromZero() = runBlocking {
        val spec = spec()
        partFile(spec).writeBytes(payload.copyOfRange(0, 900))

        server.enqueue(MockResponse().setResponseCode(416))
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))

        val outcome = downloader().download(spec)

        assertTrue("expected success, got $outcome", outcome is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())
        assertEquals("bytes=900-", server.takeRequest().getHeader("Range"))
        assertNull(server.takeRequest().getHeader("Range"))
    }

    @Test
    fun aPartialLongerThanTheResourceRestartsWithoutEvenAskingForARange() = runBlocking {
        val spec = spec()
        partFile(spec).writeBytes(ByteArray(payload.size * 2))

        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))

        val outcome = downloader().download(spec)

        assertTrue("expected success, got $outcome", outcome is DownloadOutcome.Success)
        assertNull(server.takeRequest().getHeader("Range"))
    }

    // MARK: - Integrity

    @Test
    fun aTruncatedBodyIsRejectedOnSizeAndThePartialIsDeleted() = runBlocking {
        val short = payload.copyOfRange(0, payload.size / 4)
        // A complete, well-formed HTTP response that simply does not carry the file.
        // iOS's Content-Length check would agree with the server and accept it.
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(short)))
        val spec = spec()

        val outcome = downloader().download(spec)

        assertTrue(outcome is DownloadOutcome.Failure)
        val failure = outcome as DownloadOutcome.Failure
        assertTrue(failure.reason.contains("incomplete"))
        assertFalse("a known-bad partial must not be kept for a resume", failure.partialKept)
        assertFalse(partFile(spec).exists())
        assertFalse(destinationFile(spec).exists())
    }

    @Test
    fun aRightSizedFileWithTheWrongDigestIsRejectedAndDeleted() = runBlocking {
        val impostor = ByteArray(payload.size) { 0x5A }
        assertEquals(payload.size, impostor.size)
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(impostor)))
        val spec = spec()

        val outcome = downloader().download(spec)

        assertTrue(outcome is DownloadOutcome.Failure)
        val failure = outcome as DownloadOutcome.Failure
        assertTrue(failure.reason.contains("security check"))
        assertFalse(partFile(spec).exists())
        // Never renamed into place, so the locator cannot pick it up.
        assertFalse(destinationFile(spec).exists())
    }

    @Test
    fun aResumedTransferHashesTheWholeFileNotOnlyTheAppendedTail() = runBlocking {
        val spec = spec()
        val half = payload.size / 2
        // A partial whose bytes are the wrong ones. The tail will arrive correctly, so
        // only a digest over the whole file can catch this.
        partFile(spec).writeBytes(ByteArray(half) { 0x11 })

        server.enqueue(
            MockResponse()
                .setResponseCode(206)
                .setBody(bodyOf(payload.copyOfRange(half, payload.size)))
                .setHeader("Content-Range", "bytes $half-${payload.size - 1}/${payload.size}")
        )

        val outcome = downloader().download(spec)

        assertTrue("a poisoned prefix must be caught, got $outcome", outcome is DownloadOutcome.Failure)
        assertTrue((outcome as DownloadOutcome.Failure).reason.contains("security check"))
        assertFalse(destinationFile(spec).exists())
    }

    @Test
    fun aVerifiedDownloadIsRecordedInTheTrustStore() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))
        val spec = spec()

        downloader().download(spec)

        // The record is what later lets an external copy of this same file be verified
        // instead of refused. It is written only after the digest matched.
        assertEquals(payloadDigest, TrustedDigests(modelsDir).digestFor(spec.fileName))
    }

    @Test
    fun aSuccessfulDownloadReplacesAnExistingFileAtomically() = runBlocking {
        val spec = spec()
        destinationFile(spec).writeBytes(ByteArray(10))
        server.enqueue(MockResponse().setResponseCode(200).setBody(bodyOf(payload)))

        val outcome = downloader().download(spec)

        assertTrue(outcome is DownloadOutcome.Success)
        assertArrayEquals(payload, destinationFile(spec).readBytes())
    }

    // MARK: - HTTP failures

    @Test
    fun a404IsReportedAsNotWorthRetrying() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(404))

        val outcome = downloader().download(spec())

        assertTrue(outcome is DownloadOutcome.Failure)
        val failure = outcome as DownloadOutcome.Failure
        assertFalse("telling the user to tap Retry forever would be a lie", failure.retryable)
        assertTrue(failure.reason.contains("download address"))
    }

    @Test
    fun aServerErrorIsReportedAsRetryable() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(503))

        val outcome = downloader().download(spec())

        assertTrue(outcome is DownloadOutcome.Failure)
        assertTrue((outcome as DownloadOutcome.Failure).retryable)
    }

    // MARK: - Header parsing

    @Test
    fun contentRangeParsingHandlesTheFormsAServerActuallySends() {
        assertEquals(1024L, ModelFileDownloader.parseContentRangeStart("bytes 1024-2047/4096"))
        assertEquals(4096L, ModelFileDownloader.parseContentRangeTotal("bytes 1024-2047/4096"))
        assertEquals(0L, ModelFileDownloader.parseContentRangeStart("bytes 0-0/1"))
        // An unknown total is spelled with a star and must not parse to a number.
        assertNull(ModelFileDownloader.parseContentRangeTotal("bytes 0-99/*"))
        assertNull(ModelFileDownloader.parseContentRangeStart(null))
        assertNull(ModelFileDownloader.parseContentRangeTotal(null))
        assertNull(ModelFileDownloader.parseContentRangeStart("nonsense"))
    }

    @Test
    fun theSpecForEachShippedFileCarriesTheContractsDigestAndSize() {
        // The engine's strictness is only as good as what it compares against, so the
        // two shipped specs must still be the ones shared/runtime/model-files.md pins.
        val weights = com.philwoolley.dinnerdecider.contract.ModelDownloadFile.WEIGHTS.toSpec()
        assertEquals(3_717_115_008L, weights.exactBytes)
        assertEquals(
            "dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7",
            weights.sha256,
        )
        val mmproj = com.philwoolley.dinnerdecider.contract.ModelDownloadFile.MMPROJ.toSpec()
        assertEquals(559_874_816L, mmproj.exactBytes)
        assertEquals(
            "197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1",
            mmproj.sha256,
        )
        assertNotNull(mmproj.url.takeIf { it.startsWith("https://huggingface.co/") })
    }
}
