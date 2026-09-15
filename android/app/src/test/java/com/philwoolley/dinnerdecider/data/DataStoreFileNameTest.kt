package com.philwoolley.dinnerdecider.data

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import com.philwoolley.dinnerdecider.contract.TasteProfile
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.io.File

/**
 * The DataStore file name is a **security contract**, and this is the test that
 * enforces it.
 *
 * `res/xml/backup_rules.xml` and `res/xml/data_extraction_rules.xml` exclude
 * exactly `datastore/taste_profile.preferences_pb` from Auto Backup. Everything in
 * `filesDir` that is not named in those excludes goes to the user's Google Drive
 * by default, and this file carries **allergies** — health-adjacent PII — both in
 * the taste-profile blob and in the free-text `allergies` preference.
 *
 * A rename, or a second preferences file added later with a different name, breaks
 * the exclusion silently: no error, no warning, nothing in the app looks wrong,
 * and allergies start syncing to Drive. The only detector is an assertion on the
 * path the app actually writes.
 *
 * The same rule will apply to `datastore/kroger_tokens.preferences_pb` when plan
 * Step 59 lands; that one carries OAuth tokens.
 */
@RunWith(RobolectricTestRunner::class)
class DataStoreFileNameTest {

    private val context: Context get() = ApplicationProvider.getApplicationContext()

    /** The paths the backup rules name, verbatim. */
    private val excludedByBackupRules = setOf(
        "datastore/taste_profile.preferences_pb",
        "datastore/kroger_tokens.preferences_pb",
    )

    @Test
    fun theStoreNameProducesAPathTheBackupRulesExclude() {
        val relativePath = "datastore/$DATASTORE_NAME.preferences_pb"
        assertTrue(
            "the DataStore writes $relativePath, which the backup rules do not exclude",
            relativePath in excludedByBackupRules,
        )
        assertEquals("taste_profile", DATASTORE_NAME)
    }

    /**
     * Writes through the real `Context` delegate — **both** stores, in one test on
     * purpose. The delegate creates its `DataStore` once per process, so splitting
     * this across two tests would have the second one reuse the first's instance
     * while Robolectric had already handed it a fresh `filesDir`. One test, both
     * writers, one assertion about what is on disk.
     */
    @Test
    fun bothStoresWriteToExactlyTheOneExcludedFile() = runBlocking {
        TasteProfileStore(context).save(TasteProfile(allergies = listOf("Peanuts")))
        // PrefKey.ALLERGIES is free-text allergies and lives in the ordinary
        // preference set. If it had its own file, that file would reach Drive.
        PreferencesStore(context).setAllergies("peanuts, shellfish")

        val expected = File(context.filesDir, "datastore/$DATASTORE_NAME.preferences_pb")
        assertTrue("expected ${expected.path} to exist", expected.isFile)

        val written = expected.parentFile!!
            .listFiles()
            ?.filter { it.name.endsWith(".preferences_pb") }
            ?.map { "datastore/${it.name}" }
            ?.sorted()
            .orEmpty()
        assertEquals(listOf("datastore/$DATASTORE_NAME.preferences_pb"), written)
        assertTrue(
            "unexcluded DataStore files found: ${written - excludedByBackupRules}",
            excludedByBackupRules.containsAll(written),
        )
    }
}
