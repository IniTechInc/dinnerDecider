package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.util.UUID

/**
 * Port of `DinnerDeciderTests/PhotoBatchTests.swift` — all **11** tests, Swift
 * names preserved.
 *
 * Extracted from the capture screen so the "remove a photo, then add another"
 * crash can be reproduced and fixed in isolation: no UI, no camera, no model.
 *
 * Runs on the **host JVM** under Robolectric, which is enough because the batch
 * only ever holds bitmap references — it never reads a pixel.
 */
@RunWith(RobolectricTestRunner::class)
class PhotoBatchTest {

    /** A tiny distinct 1x1 bitmap so each photo is a real [Bitmap]. */
    private fun makeImage(shade: Int = 0x808080): Bitmap =
        Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888).apply {
            setPixel(0, 0, 0xFF000000.toInt() or shade)
        }

    // MARK: - Basics

    @Test
    fun testAppendGrowsBatch() {
        var batch = PhotoBatch()
        assertTrue(batch.isEmpty)
        batch = batch.append(makeImage())
        batch = batch.append(makeImage())
        assertEquals(2, batch.count)
        assertEquals(2, batch.bitmaps.size)
    }

    @Test
    fun testAppendContentsOfPreservesOrder() {
        val a = makeImage(0x101010)
        val b = makeImage(0xE0E0E0)
        val batch = PhotoBatch().appendAll(listOf(a, b))
        assertSame(a, batch.bitmaps.first())
        assertSame(b, batch.bitmaps.last())
    }

    @Test
    fun testEachAppendedPhotoGetsADistinctIdentity() {
        val batch = PhotoBatch().append(makeImage()).append(makeImage())
        assertNotEquals(batch.photos[0].id, batch.photos[1].id)
    }

    // MARK: - Removal by identity

    @Test
    fun testRemoveByIdentityRemovesExactlyThatPhoto() {
        var batch = PhotoBatch().append(makeImage()).append(makeImage()).append(makeImage())
        val middleId = batch.photos[1].id
        batch = batch.remove(middleId)
        assertEquals(2, batch.count)
        assertFalse(batch.photos.any { it.id == middleId })
    }

    @Test
    fun testRemoveByIdentityIsNoOpForUnknownID() {
        var batch = PhotoBatch().append(makeImage())
        batch = batch.remove(UUID.randomUUID()) // never in the batch
        assertEquals(1, batch.count)
    }

    /**
     * The user's exact path: remove one, then the identity of a still-present photo
     * must survive so its own remove button still targets the right one.
     */
    @Test
    fun testRemoveByIdentityAfterAnotherRemovalTargetsCorrectPhoto() {
        var batch = PhotoBatch().append(makeImage()).append(makeImage()).append(makeImage())
        val firstId = batch.photos[0].id
        val lastId = batch.photos[2].id
        batch = batch.remove(firstId) // list shifts: [1, 2]
        batch = batch.remove(lastId) // identity still finds the original last
        assertEquals(1, batch.count)
        assertFalse(batch.photos.any { it.id == lastId })
    }

    // MARK: - Removal by index (the crash)

    /**
     * Reproduces the reported crash. A row's overlay button captures the index at
     * render time. After one photo is removed the list shrinks, but a still-live
     * closure (mid-animation, or a quick second tap) can fire with an index that is
     * now out of range. The old code called `Array.remove(at:)` directly, which
     * TRAPS; `List.removeAt` would throw `IndexOutOfBoundsException` here. This must
     * be a safe no-op instead.
     */
    @Test
    fun testRemoveWithStaleOutOfRangeIndexDoesNotCrash() {
        var batch = PhotoBatch().append(makeImage()).append(makeImage()) // valid indices 0, 1
        batch = batch.removeAt(1) // now only index 0 is valid
        batch = batch.removeAt(1) // stale index -> would throw in naive code
        assertEquals(1, batch.count)
    }

    @Test
    fun testRemoveAtValidIndexRemovesThatPhoto() {
        var batch = PhotoBatch().append(makeImage(0x202020)).append(makeImage(0xC0C0C0))
        val survivorId = batch.photos[1].id
        batch = batch.removeAt(0)
        assertEquals(1, batch.count)
        assertEquals(survivorId, batch.photos[0].id)
    }

    @Test
    fun testRemoveLastPhotoLeavesEmptyBatch() {
        var batch = PhotoBatch().append(makeImage())
        batch = batch.remove(batch.photos[0].id)
        assertTrue(batch.isEmpty)
    }

    // MARK: - Remove then re-add

    @Test
    fun testRemoveThenReAddKeepsDistinctIdentities() {
        var batch = PhotoBatch().append(makeImage())
        val firstId = batch.photos[0].id
        batch = batch.remove(firstId)
        batch = batch.append(makeImage()) // "add another photo" after unselecting
        assertEquals(1, batch.count)
        assertNotEquals(firstId, batch.photos[0].id)
    }

    @Test
    fun testPositionIsOneBased() {
        val batch = PhotoBatch().append(makeImage()).append(makeImage())
        assertEquals(1, batch.position(batch.photos[0]))
        assertEquals(2, batch.position(batch.photos[1]))
    }
}
