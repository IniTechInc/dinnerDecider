package com.philwoolley.dinnerdecider.data

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import com.philwoolley.dinnerdecider.contract.TasteProfile
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.jsonObject
import org.junit.After
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

/**
 * Host-JVM cover for the taste-profile blob.
 *
 * The DataStore runs against a temporary file, so the real serialization and the
 * real read/write path are exercised without a device.
 */
class TasteProfileStoreTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var scope: CoroutineScope
    private lateinit var dataStore: DataStore<Preferences>
    private lateinit var store: TasteProfileStore
    private lateinit var file: File

    @Before
    fun setUp() {
        // A REAL dispatcher, not a TestDispatcher: DataStore owns this scope and
        // does its file I/O on it. A StandardTestDispatcher that nothing advances
        // leaves every write pending and the test hangs.
        scope = CoroutineScope(Dispatchers.IO + SupervisorJob())
        file = File(temporaryFolder.newFolder("datastore"), "taste_profile.preferences_pb")
        dataStore = PreferenceDataStoreFactory.create(scope = scope) { file }
        store = TasteProfileStore(dataStore)
    }

    @After
    fun tearDown() {
        scope.cancel()
    }

    private fun keysOf(blob: String): Set<String> =
        Json.parseToJsonElement(blob).jsonObject.keys

    // MARK: - The trap

    /**
     * **The case that gets this wrong.**
     *
     * kotlinx.serialization omits properties equal to their default, so a
     * default-constructed profile encodes as `{}` unless `encodeDefaults = true`.
     * Swift's synthesized `Decodable` has no defaults for non-optional properties,
     * so `{}` fails the whole decode, `try?` swallows it, and the profile — with
     * the user's allergies in it — is silently gone.
     *
     * A round-trip test that only uses a *populated* profile passes either way.
     */
    @Test
    fun aDefaultConstructedProfileStillWritesAllThirteenKeys() = runBlocking {
        store.save(TasteProfile())

        val blob = store.currentRawBlob()
        assertNotNull("nothing was written", blob)
        assertEquals(
            setOf(
                "favoriteFoods", "favoriteFoodsOther",
                "dislikedFoods", "dislikedFoodsOther",
                "cuisinesLoved", "cuisinesLovedOther",
                "cuisinesAvoided", "cuisinesAvoidedOther",
                "allergies", "allergiesOther",
                "texturesAvoided", "texturesAvoidedOther",
                "spiceLevel",
            ),
            keysOf(blob!!),
        )
        assertEquals(13, keysOf(blob).size)
    }

    /**
     * The second trap. A serializer that emits `null` for an empty list fails
     * Swift's non-optional `[String]` exactly as a missing key does. Found porting
     * to Go, where a nil slice marshals to `null`.
     */
    @Test
    fun theSixListsAlwaysSerializeAsEmptyArraysNeverNull() = runBlocking {
        store.save(TasteProfile())

        val blob = Json.parseToJsonElement(store.currentRawBlob()!!).jsonObject
        listOf(
            "favoriteFoods", "dislikedFoods", "cuisinesLoved",
            "cuisinesAvoided", "allergies", "texturesAvoided",
        ).forEach { key ->
            val value = blob[key]
            assertNotNull("$key is absent", value)
            assertTrue("$key is not an array: $value", value is JsonArray)
            assertEquals("$key should be empty", 0, (value as JsonArray).size)
            assertFalse("$key must never be null", value == JsonNull)
        }
    }

    @Test
    fun aDefaultConstructedProfileRoundTripsThroughTheStore() = runBlocking {
        val original = TasteProfile()
        store.save(original)
        assertEquals(original, store.current())
        assertEquals(TasteProfile.DEFAULT_SPICE_LEVEL, store.current().spiceLevel)
        assertFalse(store.current().hasContent)
    }

    // MARK: - Ordinary round trips

    @Test
    fun aPopulatedProfileRoundTripsIntact() = runBlocking {
        val original = TasteProfile(
            favoriteFoods = listOf("Pasta", "Tacos"),
            favoriteFoodsOther = "anything grilled",
            dislikedFoods = listOf("Olives"),
            cuisinesLoved = listOf("Thai"),
            cuisinesAvoided = listOf("Fusion"),
            allergies = listOf("Peanuts", "Shellfish"),
            allergiesOther = "sesame",
            texturesAvoided = listOf("Slimy"),
            spiceLevel = 5,
        )
        store.save(original)
        assertEquals(original, store.current())
        assertEquals(13, keysOf(store.currentRawBlob()!!).size)
    }

    /**
     * Allergies are the reason any of this matters. This is the assertion that
     * would fail if someone "optimised" the writer to skip empty fields.
     */
    @Test
    fun anAllergiesOnlyProfileSurvivesAndReadsAsHavingContent() = runBlocking {
        store.save(TasteProfile(allergies = listOf("Peanuts")))

        val loaded = store.current()
        assertEquals(listOf("Peanuts"), loaded.allergies)
        assertTrue(loaded.hasContent)
        assertTrue(loaded.promptFragment().contains("Allergies: Peanuts. Never include these."))
    }

    // MARK: - Reads are forgiving

    @Test
    fun nothingStoredReadsAsADefaultProfileNotNull() = runBlocking {
        assertEquals(TasteProfile(), store.current())
        assertNull(store.currentStored())
        assertNull(store.currentRawBlob())
    }

    /**
     * Reading is deliberately more forgiving than writing: Kotlin's property
     * defaults cover a missing key, so a partial blob still yields the allergies
     * it does carry. That asymmetry is safe in the direction that matters. It is
     * not a licence to write one.
     */
    @Test
    fun aPartialBlobStillYieldsTheAllergiesItCarries() = runBlocking {
        dataStore.updateData { preferences ->
            preferences.toMutablePreferences().apply {
                this[TasteProfileStore.KEY] = """{"allergies":["Peanuts"]}"""
            }
        }
        assertEquals(listOf("Peanuts"), store.current().allergies)
    }

    @Test
    fun anUnreadableBlobReadsAsADefaultProfileRatherThanThrowing() = runBlocking {
        dataStore.updateData { preferences ->
            preferences.toMutablePreferences().apply {
                this[TasteProfileStore.KEY] = "not json at all"
            }
        }
        assertEquals(TasteProfile(), store.current())
        assertNull(store.currentStored())
    }

    @Test
    fun anUnknownKeyFromANewerVersionDoesNotBreakTheLoad() = runBlocking {
        val blob = TasteProfile.encode(TasteProfile(allergies = listOf("Peanuts")))
            .replaceFirst("{", """{"futureField":42,""")
        dataStore.updateData { preferences ->
            preferences.toMutablePreferences().apply { this[TasteProfileStore.KEY] = blob }
        }
        assertEquals(listOf("Peanuts"), store.current().allergies)
    }

    // MARK: - Mutation helpers

    @Test
    fun updateReadsTransformsAndWritesTheWholeBlob() = runBlocking {
        store.save(TasteProfile(allergies = listOf("Peanuts")))
        store.update { it.copy(spiceLevel = 5) }

        val loaded = store.current()
        assertEquals(5, loaded.spiceLevel)
        assertEquals(listOf("Peanuts"), loaded.allergies)
        assertEquals(13, keysOf(store.currentRawBlob()!!).size)
    }

    @Test
    fun clearRemovesTheBlobWhichIsNotTheSameAsSavingAnEmptyProfile() = runBlocking {
        store.save(TasteProfile(allergies = listOf("Peanuts")))
        assertNotNull(store.currentStored())

        store.clear()
        assertNull(store.currentStored())
        assertNull(store.currentRawBlob())
        assertEquals(TasteProfile(), store.current())
    }

    @Test
    fun theProfileFlowEmitsTheLatestWrite() = runBlocking {
        store.save(TasteProfile(spiceLevel = 1))
        assertEquals(1, store.profile.first().spiceLevel)
        store.save(TasteProfile(spiceLevel = 4))
        assertEquals(4, store.profile.first().spiceLevel)
    }
}
