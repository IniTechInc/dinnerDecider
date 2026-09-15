package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import androidx.compose.runtime.Immutable
import java.util.UUID

/**
 * A single photo staged for a scan, carrying a stable identity.
 *
 * Port of `DinnerDecider/Services/PhotoBatch.swift:9-21`. Wrapping each bitmap in
 * an identified value is the whole point: it lets a Compose `key(...)` / `items(key
 * = ...)` track thumbnails by identity rather than by list offset. Offset-based
 * identity is what let a removed-then-re-added photo leave a stale index behind.
 *
 * Equality is **identity only**, matching the Swift `==`. Two entries wrapping the
 * same bitmap are different photos; the same entry is equal to itself after the
 * list around it has shifted.
 */
@Immutable
class BatchPhoto(
    val id: UUID = UUID.randomUUID(),
    val bitmap: Bitmap,
) {
    override fun equals(other: Any?): Boolean = this === other || (other is BatchPhoto && other.id == id)
    override fun hashCode(): Int = id.hashCode()
    override fun toString(): String = "BatchPhoto(id=$id, ${bitmap.width}x${bitmap.height})"
}

/**
 * The value-type owner of the capture batch.
 *
 * Port of `PhotoBatch.swift:26-64`. Every mutation the capture screen performs
 * (add from camera, add from the picker, remove a thumbnail) goes through here, so
 * the logic is unit-testable and **cannot crash on a stale index**.
 *
 * ## Identity-keyed, never index-keyed — this is a crash class
 *
 * The shipped iOS bug: a thumbnail's remove button captured the row's **index** at
 * render time. Remove one photo, and a still-live closure (mid-animation, or a
 * quick second tap) fires with an index that is now out of range;
 * `Array.remove(at:)` **traps**. The fix, and the contract every port must keep:
 *
 * - [remove] by id is a **no-op** when the photo is already gone.
 * - [removeAt] **ignores** an out-of-range index rather than throwing.
 * - Identities are never reused: remove then re-add yields a fresh [BatchPhoto.id],
 *   so a stale identity can never resolve to the new photo either.
 *
 * Swift's `struct` gives value semantics for free; here the type is immutable and
 * every mutation returns a new batch, which is what Compose state wants anyway
 * (`var batch by remember { mutableStateOf(PhotoBatch()) }`).
 */
@Immutable
data class PhotoBatch(val photos: List<BatchPhoto> = emptyList()) {

    val isEmpty: Boolean get() = photos.isEmpty()

    val count: Int get() = photos.size

    /** The underlying bitmaps in batch order, for the scan pipeline. */
    val bitmaps: List<Bitmap> get() = photos.map { it.bitmap }

    fun append(bitmap: Bitmap): PhotoBatch = PhotoBatch(photos + BatchPhoto(bitmap = bitmap))

    /** Appends in the given order; each bitmap gets its own fresh identity. */
    fun appendAll(bitmaps: List<Bitmap>): PhotoBatch =
        PhotoBatch(photos + bitmaps.map { BatchPhoto(bitmap = it) })

    /**
     * Remove the photo with this identity. A **no-op** when it is already gone, so
     * a double tap or a stale recomposition can never fail.
     */
    fun remove(id: UUID): PhotoBatch {
        if (photos.none { it.id == id }) return this
        return PhotoBatch(photos.filterNot { it.id == id })
    }

    /**
     * Remove by list position. Out-of-range indices are **ignored** rather than
     * throwing, so a stale index captured by a row closure is harmless.
     */
    fun removeAt(index: Int): PhotoBatch {
        if (index !in photos.indices) return this
        return PhotoBatch(photos.filterIndexed { i, _ -> i != index })
    }

    fun clear(): PhotoBatch = if (photos.isEmpty()) this else PhotoBatch()

    /**
     * The **1-based** position of a photo, for accessibility labels ("Photo 1 of
     * 3"). An unknown photo reports 1, matching Swift's `(firstIndex ?? 0) + 1`.
     */
    fun position(photo: BatchPhoto): Int = position(photo.id)

    /** 1-based position by identity; 1 for an unknown id, as on iOS. */
    fun position(id: UUID): Int {
        val index = photos.indexOfFirst { it.id == id }
        return (if (index < 0) 0 else index) + 1
    }
}
