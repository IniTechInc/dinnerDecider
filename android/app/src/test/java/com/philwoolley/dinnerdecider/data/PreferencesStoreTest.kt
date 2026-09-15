package com.philwoolley.dinnerdecider.data

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import com.philwoolley.dinnerdecider.contract.DietPreference
import com.philwoolley.dinnerdecider.contract.InventorySort
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import com.philwoolley.dinnerdecider.contract.PrefKey
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

/**
 * Host-JVM cover for the twelve preference keys.
 *
 * Runs against a real DataStore over a temporary file, so key names, defaults and
 * the read/write path are all genuinely exercised.
 */
class PreferencesStoreTest {

    @get:Rule
    val temporaryFolder = TemporaryFolder()

    private lateinit var scope: CoroutineScope
    private lateinit var dataStore: DataStore<Preferences>
    private lateinit var store: PreferencesStore

    @Before
    fun setUp() {
        // A REAL dispatcher: DataStore owns this scope and does file I/O on it.
        scope = CoroutineScope(Dispatchers.IO + SupervisorJob())
        val file = File(temporaryFolder.newFolder("datastore"), "taste_profile.preferences_pb")
        dataStore = PreferenceDataStoreFactory.create(scope = scope) { file }
        store = PreferencesStore(dataStore)
    }

    @After
    fun tearDown() = scope.cancel()

    // MARK: - Key names are the iOS UserDefaults names

    /**
     * The key strings are the cross-platform contract, not the storage mechanism.
     * A typo here is invisible: the write succeeds, the read returns the default,
     * and the user's setting appears not to stick.
     */
    @Test
    fun everyKeyUsesTheSharedContractName() {
        assertEquals("dietPreference", PreferencesStore.Keys.DIET.name)
        assertEquals("allergies", PreferencesStore.Keys.ALLERGIES.name)
        assertEquals("cuisineLikes", PreferencesStore.Keys.CUISINE_LIKES.name)
        assertEquals("householdSize", PreferencesStore.Keys.HOUSEHOLD_SIZE.name)
        assertEquals("hasSeenModelSetup", PreferencesStore.Keys.HAS_SEEN_MODEL_SETUP.name)
        assertEquals("hasSeenOnboarding", PreferencesStore.Keys.HAS_SEEN_ONBOARDING.name)
        assertEquals("inventorySortOrder", PreferencesStore.Keys.INVENTORY_SORT.name)
        assertEquals("debugSimulateRecipeFailure", PreferencesStore.Keys.DEBUG_SIMULATE_FAILURE.name)
        assertEquals(
            "hasCompletedTasteProfile",
            PreferencesStore.Keys.HAS_COMPLETED_TASTE_PROFILE.name,
        )
        assertEquals("selectedModelFileName", PreferencesStore.Keys.SELECTED_MODEL.name)
        assertEquals("krogerStoreId", PreferencesStore.Keys.KROGER_STORE_ID.name)
        assertEquals("krogerStoreName", PreferencesStore.Keys.KROGER_STORE_NAME.name)
    }

    @Test
    fun theKeyNamesComeFromTheContractObjectsNotFromLiteralsHere() {
        assertEquals(PrefKey.DIET, PreferencesStore.Keys.DIET.name)
        assertEquals(PrefKey.ALLERGIES, PreferencesStore.Keys.ALLERGIES.name)
        assertEquals(
            ModelFileSelection.SELECTED_MODEL_KEY,
            PreferencesStore.Keys.SELECTED_MODEL.name,
        )
    }

    // MARK: - Defaults on a virgin store

    @Test
    fun aVirginStoreReadsEveryContractDefault() = runBlocking {
        assertEquals(DietPreference.NONE, store.diet.first())
        assertEquals("", store.allergies.first())
        assertEquals("", store.cuisineLikes.first())
        assertEquals(PrefKey.DEFAULT_HOUSEHOLD_SIZE, store.householdSize.first())
        assertFalse(store.hasSeenOnboarding.first())
        assertFalse(store.hasSeenModelSetup.first())
        assertFalse(store.hasCompletedTasteProfile.first())
        assertEquals(InventorySort.CATEGORY, store.inventorySort.first())
        assertFalse(store.debugSimulateRecipeFailure.first())
        assertNull(store.selectedModelFileName.first())
        assertEquals("", store.krogerStoreId.first())
        assertEquals("", store.krogerStoreName.first())
    }

    /**
     * `0` is the "never set" marker, not a household of nobody
     * (`RecipePreferences.current`). Using it literally would divide portions by
     * zero downstream.
     */
    @Test
    fun aStoredHouseholdSizeOfZeroMeansNeverSet() = runBlocking {
        assertEquals(2, PreferencesStore.resolveHouseholdSize(null))
        assertEquals(2, PreferencesStore.resolveHouseholdSize(0))
        assertEquals(1, PreferencesStore.resolveHouseholdSize(1))
        assertEquals(6, PreferencesStore.resolveHouseholdSize(6))

        store.setHouseholdSize(0)
        assertEquals(PrefKey.DEFAULT_HOUSEHOLD_SIZE, store.householdSize.first())
    }

    // MARK: - Round trips

    @Test
    fun recipeShapingPreferencesRoundTrip() = runBlocking {
        store.setDiet(DietPreference.GLUTEN_FREE)
        store.setAllergies("peanuts, shellfish")
        store.setCuisineLikes("thai, italian")
        store.setHouseholdSize(4)

        assertEquals(DietPreference.GLUTEN_FREE, store.diet.first())
        val snapshot = store.currentRecipePreferences()
        // glutenFree is camelCase, unlike every FoodCategory value. Shipped data.
        assertEquals("glutenFree", snapshot.diet)
        assertEquals("peanuts, shellfish", snapshot.allergies)
        assertEquals("thai, italian", snapshot.cuisines)
        assertEquals(4, snapshot.householdSize)
    }

    @Test
    fun flagsRoundTrip() = runBlocking {
        store.setHasSeenOnboarding(true)
        store.setHasSeenModelSetup(true)
        store.setHasCompletedTasteProfile(true)
        store.setDebugSimulateRecipeFailure(true)

        assertTrue(store.hasSeenOnboarding.first())
        assertTrue(store.hasSeenModelSetup.first())
        assertTrue(store.hasCompletedTasteProfile.first())
        assertTrue(store.debugSimulateRecipeFailure.first())
    }

    @Test
    fun theInventorySortRoundTripsAndAnUnknownValueFallsBackToCategory() = runBlocking {
        store.setInventorySort(InventorySort.DATE_ADDED)
        assertEquals(InventorySort.DATE_ADDED, store.inventorySort.first())

        dataStore.updateData { preferences ->
            preferences.toMutablePreferences()
                .apply { this[PreferencesStore.Keys.INVENTORY_SORT] = "byVibes" }
        }
        assertEquals(InventorySort.CATEGORY, store.inventorySort.first())
    }

    @Test
    fun anUnknownDietValueFallsBackToNone() = runBlocking {
        dataStore.updateData { preferences ->
            preferences.toMutablePreferences()
                .apply { this[PreferencesStore.Keys.DIET] = "carnivore" }
        }
        assertEquals(DietPreference.NONE, store.diet.first())
    }

    // MARK: - Model pin

    /**
     * Settings must surface a pin with a one-tap clear: a user who pinned the
     * memory-hungry quant keeps it even after installing a smaller one, because
     * `chooseModelFile` honours the pin. Clearing must actually remove the key,
     * not store an empty string that a later `isNullOrBlank` check might miss.
     */
    @Test
    fun theModelPinIsSetAndClearedCleanly() = runBlocking {
        store.setSelectedModelFileName("gemma-4-E4B-it-UD-IQ3_XXS.gguf")
        assertEquals("gemma-4-E4B-it-UD-IQ3_XXS.gguf", store.currentSelectedModelFileName())

        store.setSelectedModelFileName(null)
        assertNull(store.currentSelectedModelFileName())

        store.setSelectedModelFileName("something.gguf")
        store.setSelectedModelFileName("   ")
        assertNull(store.currentSelectedModelFileName())
    }

    // MARK: - Kroger store

    @Test
    fun theKrogerStoreIsWrittenAndClearedAsAPair() = runBlocking {
        store.setKrogerStore("01400376", "Fred Meyer - Burlington")
        assertEquals("01400376", store.krogerStoreId.first())
        assertEquals("Fred Meyer - Burlington", store.krogerStoreName.first())

        store.clearKrogerStore()
        assertEquals("", store.krogerStoreId.first())
        assertEquals("", store.krogerStoreName.first())
    }

    // MARK: - Coexistence with the taste profile blob

    /**
     * Both stores share one DataStore file (see [PreferencesStore]'s KDoc on why
     * that is a security decision). Writing one must not disturb the other.
     */
    @Test
    fun theTasteProfileBlobAndThePreferenceKeysCoexistInOneFile() = runBlocking {
        val tasteProfile = TasteProfileStore(dataStore)
        tasteProfile.save(
            com.philwoolley.dinnerdecider.contract.TasteProfile(allergies = listOf("Peanuts")),
        )
        store.setAllergies("peanuts")
        store.setDiet(DietPreference.VEGAN)

        assertEquals(listOf("Peanuts"), tasteProfile.current().allergies)
        assertEquals("peanuts", store.allergies.first())
        assertEquals(DietPreference.VEGAN, store.diet.first())
    }
}
