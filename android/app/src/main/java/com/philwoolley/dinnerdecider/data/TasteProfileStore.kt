package com.philwoolley.dinnerdecider.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import com.philwoolley.dinnerdecider.contract.TasteProfile
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

/**
 * The taste profile, stored as a **JSON blob** under the key `tasteProfile`.
 *
 * Shares [dinnerDeciderDataStore] with [PreferencesStore]; see that file for why
 * there is exactly one DataStore and why its name is a security contract.
 *
 * ## Write all thirteen keys, every time, and never `null` for a list
 *
 * Unlike the database models, this schema **is** the real on-disk shape: iOS
 * encodes the struct with `JSONEncoder` and stores the blob under the same key.
 * Two independent traps have the same consequence — the entire profile is lost,
 * **allergies included** — and neither one fails a port's own round-trip test,
 * because both are only visible to the *other* platform's decoder:
 *
 * 1. **A missing key.** Swift's synthesized `Decodable` does not fall back to a
 *    property's default for a missing key on a non-optional property. A partial
 *    blob fails to decode entirely, `TasteProfile.load()` swallows the error with
 *    `try?`, and the profile is silently gone.
 * 2. **A `null` list.** The six list properties are `"type": "array"` and Swift
 *    decodes them into non-optional `[String]`, so a JSON `null` fails exactly as
 *    a missing key does. Found the hard way porting to Go, where a nil slice
 *    marshals to `null`: the Go writer passed its own round trip and produced a
 *    blob iOS could not read.
 *
 * Both are already handled by `TasteProfile.json`, which sets
 * `encodeDefaults = true` — kotlinx's default omits properties equal to their
 * default, so a **default-constructed profile would encode as `{}`**. That is
 * precisely the case a careless test never covers, so
 * `TasteProfileStoreTest.aDefaultConstructedProfileStillWritesAllThirteenKeys`
 * covers it.
 *
 * This store therefore never hand-rolls a writer: it calls
 * [TasteProfile.encode]/[TasteProfile.decode] and nothing else.
 */
class TasteProfileStore(private val dataStore: DataStore<Preferences>) {

    constructor(context: Context) : this(context.applicationContext.dinnerDeciderDataStore)

    /**
     * The stored profile, or a **default-constructed one** when nothing has been
     * saved or the blob is unreadable.
     *
     * Never null, so the prompt builder has no null branch: an empty profile's
     * `hasContent` is false and it contributes no fragment, which is the same
     * outcome with fewer ways to go wrong.
     */
    val profile: Flow<TasteProfile> = dataStore.data.map { preferences ->
        preferences[KEY]?.let(TasteProfile::decode) ?: TasteProfile()
    }

    /** Null when nothing is stored, for callers that need to tell the two apart. */
    val storedProfile: Flow<TasteProfile?> = dataStore.data.map { preferences ->
        preferences[KEY]?.let(TasteProfile::decode)
    }

    /** The raw blob, for the export/diagnostics path and for tests. */
    val rawBlob: Flow<String?> = dataStore.data.map { it[KEY] }

    suspend fun current(): TasteProfile = profile.first()

    suspend fun currentStored(): TasteProfile? = storedProfile.first()

    suspend fun currentRawBlob(): String? = rawBlob.first()

    /**
     * Save the profile.
     *
     * Always through [TasteProfile.encode], so all thirteen keys are present and
     * the six lists render as `[]` rather than `null` — including for a profile
     * the user never touched.
     */
    suspend fun save(profile: TasteProfile) {
        val blob = TasteProfile.encode(profile)
        dataStore.edit { it[KEY] = blob }
    }

    /**
     * Read, transform, write. Convenience for the wizard's per-page edits, which
     * would otherwise each need a read and a save and could drop a field in
     * between.
     */
    suspend fun update(transform: (TasteProfile) -> TasteProfile) {
        save(transform(current()))
    }

    /** Remove the blob entirely. Distinct from saving an empty profile. */
    suspend fun clear() {
        dataStore.edit { it.remove(KEY) }
    }

    companion object {
        /** `tasteProfile`, matching iOS. [TasteProfile.STORAGE_KEY]. */
        val KEY = stringPreferencesKey(TasteProfile.STORAGE_KEY)
    }
}
