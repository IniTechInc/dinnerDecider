package com.philwoolley.dinnerdecider.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

/**
 * Host-JVM cover for the two persistence decisions in [Converters]. Pure
 * functions, no database, no device.
 */
class ConvertersTest {

    // MARK: - Instant <-> epoch millis

    @Test
    fun instantsRoundTripExactlyAtMillisecondPrecision() {
        val instant = Instant.ofEpochMilli(1_757_808_000_123L)
        val stored = Converters.instantToEpochMillis(instant)
        assertEquals(1_757_808_000_123L, stored)
        assertEquals(instant, Converters.epochMillisToInstant(stored))
    }

    @Test
    fun theEpochAndPreEpochInstantsSurvive() {
        listOf(0L, -1L, -86_400_000L, 1L).forEach { millis ->
            val instant = Instant.ofEpochMilli(millis)
            assertEquals(instant, Converters.epochMillisToInstant(Converters.instantToEpochMillis(instant)))
        }
    }

    @Test
    fun nullRoundTripsAsNullSoANullableColumnStaysNullable() {
        assertNull(Converters.instantToEpochMillis(null))
        assertNull(Converters.epochMillisToInstant(null))
    }

    /**
     * Epoch millis sort as integers, which is the whole reason for the choice:
     * the `dateAdded` inventory sort and the shopping list's oldest-first order
     * are both plain SQL `ORDER BY` over this column.
     */
    @Test
    fun storedValuesOrderTheSameWayTheInstantsDo() {
        val early = Instant.ofEpochMilli(1_000L)
        val late = Instant.ofEpochMilli(2_000L)
        assertTrue(early.isBefore(late))
        assertTrue(Converters.instantToEpochMillis(early)!! < Converters.instantToEpochMillis(late)!!)
    }

    // MARK: - List<String> <-> JSON array

    @Test
    fun stringListsRoundTripAsJsonArrays() {
        val steps = listOf("Preheat the oven", "Chop the onion", "Simmer for 20 minutes")
        val stored = Converters.stringListToJson(steps)
        assertTrue(stored.startsWith("[") && stored.endsWith("]"))
        assertEquals(steps, Converters.jsonToStringList(stored))
    }

    @Test
    fun anEmptyListStoresAsAnEmptyArrayNotNullAndNotAnEmptyString() {
        assertEquals("[]", Converters.stringListToJson(emptyList()))
        assertEquals("[]", Converters.stringListToJson(null))
        assertEquals(emptyList<String>(), Converters.jsonToStringList("[]"))
    }

    /**
     * The reason this is JSON rather than a delimiter join. Cooking steps are free
     * text written by a language model; every plausible delimiter appears in them.
     */
    @Test
    fun stepsContainingDelimitersAndQuotesSurviveIntact() {
        val steps = listOf(
            "Add salt, pepper, and thyme",
            "Season | to taste",
            """Stir in the "secret" sauce""",
            "Cook 5-7 min;\nthen rest",
        )
        assertEquals(steps, Converters.jsonToStringList(Converters.stringListToJson(steps)))
    }

    @Test
    fun orderIsPreservedBecauseStepsAreOrdered() {
        val steps = (1..10).map { "Step $it" }
        assertEquals(steps, Converters.jsonToStringList(Converters.stringListToJson(steps)))
    }

    @Test
    fun anUnreadableBlobYieldsAnEmptyListRatherThanTakingDownTheScreen() {
        assertEquals(emptyList<String>(), Converters.jsonToStringList("not json at all"))
        assertEquals(emptyList<String>(), Converters.jsonToStringList("""{"not":"an array"}"""))
        assertEquals(emptyList<String>(), Converters.jsonToStringList("[1, 2, 3]"))
        assertEquals(emptyList<String>(), Converters.jsonToStringList(""))
        assertEquals(emptyList<String>(), Converters.jsonToStringList(null))
    }
}
