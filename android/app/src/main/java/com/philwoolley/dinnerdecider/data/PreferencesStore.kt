package com.philwoolley.dinnerdecider.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.philwoolley.dinnerdecider.contract.DietPreference
import com.philwoolley.dinnerdecider.contract.InventorySort
import com.philwoolley.dinnerdecider.contract.ModelFileSelection
import com.philwoolley.dinnerdecider.contract.PrefKey
import com.philwoolley.dinnerdecider.contract.RecipePreferences
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

/**
 * The app's single DataStore file, and the reason it is single.
 *
 * ## The file name is a security contract
 *
 * `res/xml/backup_rules.xml` and `res/xml/data_extraction_rules.xml` exclude
 * exactly `datastore/taste_profile.preferences_pb` (and, for plan Step 59,
 * `datastore/kroger_tokens.preferences_pb`) from Auto Backup. Anything in
 * `filesDir` that is **not** named in those excludes goes to the user's Google
 * Drive by default.
 *
 * [PrefKey.ALLERGIES] is free-text allergies — health-adjacent PII — and it is
 * part of the ordinary preference set. If the general preferences lived in a
 * second DataStore file with any other name, that file would be backed up, and
 * allergies would reach Drive while the *taste profile's* copy of them was
 * carefully excluded. The mismatch would be silent: no error, no warning, and
 * nothing in the app would look wrong.
 *
 * So there is **one** store, named [DATASTORE_NAME], and both [PreferencesStore]
 * and [TasteProfileStore] read and write it. Adding a second preferences file
 * means adding a matching exclude to both XML rule files in the same change.
 */
internal val Context.dinnerDeciderDataStore: DataStore<Preferences> by preferencesDataStore(
    name = DATASTORE_NAME,
)

/**
 * The DataStore file's base name. Produces
 * `filesDir/datastore/taste_profile.preferences_pb`, which is exactly what the
 * backup rules exclude. **Changing this silently starts backing up allergies.**
 */
const val DATASTORE_NAME: String = "taste_profile"

/**
 * Typed access to the twelve preference keys shared between Settings, onboarding,
 * the prompt builder and the Kroger screens.
 *
 * The key strings come from [PrefKey] and [ModelFileSelection.SELECTED_MODEL_KEY]
 * and are identical to iOS's `UserDefaults` key names, so the two ports describe
 * the same user state. The storage mechanism behind them is a platform detail;
 * the names are not.
 *
 * Every read tolerates a missing key by falling back to the same default iOS
 * falls back to — including the one that is not obvious: a stored household size
 * of `0` is the *"never set"* marker and maps to
 * [PrefKey.DEFAULT_HOUSEHOLD_SIZE], not to a household of nobody.
 */
class PreferencesStore(private val dataStore: DataStore<Preferences>) {

    constructor(context: Context) : this(context.applicationContext.dinnerDeciderDataStore)

    private val data: Flow<Preferences> get() = dataStore.data

    // MARK: - Recipe-shaping preferences

    val diet: Flow<DietPreference> =
        data.map { DietPreference.fromStorage(it[Keys.DIET] ?: DietPreference.NONE.raw) }

    val allergies: Flow<String> = data.map { it[Keys.ALLERGIES].orEmpty() }

    val cuisineLikes: Flow<String> = data.map { it[Keys.CUISINE_LIKES].orEmpty() }

    val householdSize: Flow<Int> = data.map { resolveHouseholdSize(it[Keys.HOUSEHOLD_SIZE]) }

    /**
     * The snapshot the recipe prompt builder consumes. Assembled through
     * [RecipePreferences.fromStored] so the fallbacks live in one place.
     */
    val recipePreferences: Flow<RecipePreferences> = data.map { preferences ->
        RecipePreferences.fromStored(
            diet = preferences[Keys.DIET],
            allergies = preferences[Keys.ALLERGIES],
            cuisines = preferences[Keys.CUISINE_LIKES],
            householdSize = preferences[Keys.HOUSEHOLD_SIZE] ?: 0,
        )
    }

    suspend fun currentRecipePreferences(): RecipePreferences = recipePreferences.first()

    suspend fun setDiet(value: DietPreference) = put(Keys.DIET, value.raw)

    suspend fun setAllergies(value: String) = put(Keys.ALLERGIES, value)

    suspend fun setCuisineLikes(value: String) = put(Keys.CUISINE_LIKES, value)

    suspend fun setHouseholdSize(value: Int) = put(Keys.HOUSEHOLD_SIZE, value.coerceAtLeast(0))

    // MARK: - First-run and UI flags

    val hasSeenOnboarding: Flow<Boolean> = data.map { it[Keys.HAS_SEEN_ONBOARDING] ?: false }

    val hasSeenModelSetup: Flow<Boolean> = data.map { it[Keys.HAS_SEEN_MODEL_SETUP] ?: false }

    val hasCompletedTasteProfile: Flow<Boolean> =
        data.map { it[Keys.HAS_COMPLETED_TASTE_PROFILE] ?: false }

    val inventorySort: Flow<InventorySort> =
        data.map { InventorySort.fromStorage(it[Keys.INVENTORY_SORT] ?: InventorySort.CATEGORY.raw) }

    /** Hidden debug switch that forces a malformed recipe response. Off in normal use. */
    val debugSimulateRecipeFailure: Flow<Boolean> =
        data.map { it[Keys.DEBUG_SIMULATE_FAILURE] ?: false }

    suspend fun setHasSeenOnboarding(value: Boolean) = put(Keys.HAS_SEEN_ONBOARDING, value)

    suspend fun setHasSeenModelSetup(value: Boolean) = put(Keys.HAS_SEEN_MODEL_SETUP, value)

    suspend fun setHasCompletedTasteProfile(value: Boolean) =
        put(Keys.HAS_COMPLETED_TASTE_PROFILE, value)

    suspend fun setInventorySort(value: InventorySort) = put(Keys.INVENTORY_SORT, value.raw)

    suspend fun setDebugSimulateRecipeFailure(value: Boolean) =
        put(Keys.DEBUG_SIMULATE_FAILURE, value)

    // MARK: - Model selection

    /**
     * The user's pinned weights filename, or null for "choose automatically".
     *
     * A pin wins even when it names the memory-hungry quant, and a pin naming a
     * file that is no longer present falls through to the automatic preference —
     * both rules live in `ModelFileSelection.chooseModelFile`, not here. Settings
     * must surface any pin with a one-tap clear, which is what [setSelectedModelFileName]
     * with `null` does.
     */
    val selectedModelFileName: Flow<String?> = data.map { it[Keys.SELECTED_MODEL] }

    suspend fun currentSelectedModelFileName(): String? = selectedModelFileName.first()

    suspend fun setSelectedModelFileName(value: String?) {
        dataStore.edit { preferences ->
            if (value.isNullOrBlank()) preferences.remove(Keys.SELECTED_MODEL)
            else preferences[Keys.SELECTED_MODEL] = value
        }
    }

    // MARK: - Kroger store

    /** The chosen Kroger store's id, or `""` when none is chosen. */
    val krogerStoreId: Flow<String> = data.map { it[Keys.KROGER_STORE_ID].orEmpty() }

    val krogerStoreName: Flow<String> = data.map { it[Keys.KROGER_STORE_NAME].orEmpty() }

    suspend fun setKrogerStore(id: String, name: String) {
        dataStore.edit {
            it[Keys.KROGER_STORE_ID] = id
            it[Keys.KROGER_STORE_NAME] = name
        }
    }

    suspend fun clearKrogerStore() {
        dataStore.edit {
            it.remove(Keys.KROGER_STORE_ID)
            it.remove(Keys.KROGER_STORE_NAME)
        }
    }

    // MARK: - Internals

    private suspend fun <T> put(key: Preferences.Key<T>, value: T) {
        dataStore.edit { it[key] = value }
    }

    /**
     * The preference keys, named by the **iOS `UserDefaults` key strings**. Kept
     * in one object so a typo is a compile error rather than a key that silently
     * never matches what was written.
     */
    object Keys {
        val DIET = stringPreferencesKey(PrefKey.DIET)
        val ALLERGIES = stringPreferencesKey(PrefKey.ALLERGIES)
        val CUISINE_LIKES = stringPreferencesKey(PrefKey.CUISINE_LIKES)
        val HOUSEHOLD_SIZE = intPreferencesKey(PrefKey.HOUSEHOLD_SIZE)
        val HAS_SEEN_MODEL_SETUP = booleanPreferencesKey(PrefKey.HAS_SEEN_MODEL_SETUP)
        val HAS_SEEN_ONBOARDING = booleanPreferencesKey(PrefKey.HAS_SEEN_ONBOARDING)
        val INVENTORY_SORT = stringPreferencesKey(PrefKey.INVENTORY_SORT)
        val DEBUG_SIMULATE_FAILURE = booleanPreferencesKey(PrefKey.DEBUG_SIMULATE_FAILURE)
        val HAS_COMPLETED_TASTE_PROFILE = booleanPreferencesKey(PrefKey.HAS_COMPLETED_TASTE_PROFILE)
        val SELECTED_MODEL = stringPreferencesKey(ModelFileSelection.SELECTED_MODEL_KEY)
        val KROGER_STORE_ID = stringPreferencesKey(KROGER_STORE_ID_KEY)
        val KROGER_STORE_NAME = stringPreferencesKey(KROGER_STORE_NAME_KEY)
    }

    companion object {
        /** `KrogerService.swift:135-136`, verbatim. */
        const val KROGER_STORE_ID_KEY: String = "krogerStoreId"
        const val KROGER_STORE_NAME_KEY: String = "krogerStoreName"

        /**
         * `0` is the "never set" marker, not a household of nobody
         * (`RecipePreferences.current`, `Preferences.swift:81-87`).
         */
        fun resolveHouseholdSize(stored: Int?): Int =
            if (stored == null || stored == 0) PrefKey.DEFAULT_HOUSEHOLD_SIZE else stored
    }
}
