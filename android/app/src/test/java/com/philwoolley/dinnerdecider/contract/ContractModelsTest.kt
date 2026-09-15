package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.MissingFieldException
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Decode behavior of the wire models, pinned against Swift's synthesized
 * `Decodable`.
 *
 * The `Json` configuration here matches the one the response parser uses
 * (`shared/parser/response-parsing.md`): unknown keys ignored (the schemas all say
 * `additionalProperties: true`), non-lenient, and no value coercion.
 */
@OptIn(ExperimentalSerializationApi::class)
class ContractModelsTest {

    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = false
        coerceInputValues = false
    }

    // MARK: - IdentifiedItem: brand has a default, the other three do not

    @Test
    fun `absent brand decodes to null`() {
        val item = json.decodeFromString<IdentifiedItem>(
            """{"name":"Whole Milk","category":"dairy","confidence":0.9}""",
        )
        assertEquals("Whole Milk", item.name)
        assertNull(item.brand)
        assertEquals("dairy", item.category)
        assertEquals(0.9, item.confidence, 1e-9)
    }

    @Test
    fun `explicit null brand decodes to null, identically to an absent key`() {
        val absent = json.decodeFromString<IdentifiedItem>(
            """{"name":"Whole Milk","category":"dairy","confidence":0.9}""",
        )
        val explicit = json.decodeFromString<IdentifiedItem>(
            """{"name":"Whole Milk","brand":null,"category":"dairy","confidence":0.9}""",
        )
        assertNull(explicit.brand)
        assertEquals(absent, explicit)
    }

    @Test
    fun `present brand is kept verbatim`() {
        val item = json.decodeFromString<IdentifiedItem>(
            """{"name":"Whole Milk","brand":"Horizon","category":"dairy","confidence":0.9}""",
        )
        assertEquals("Horizon", item.brand)
    }

    /**
     * No default on `name`, so a missing key throws exactly as Swift's synthesized
     * `Decodable` fails. The parser catches this and returns null; adding a default
     * would silently accept a malformed reply.
     */
    @Test
    fun `missing name throws MissingFieldException`() {
        assertThrows(MissingFieldException::class.java) {
            json.decodeFromString<IdentifiedItem>("""{"category":"dairy","confidence":0.9}""")
        }
    }

    @Test
    fun `missing category throws MissingFieldException`() {
        assertThrows(MissingFieldException::class.java) {
            json.decodeFromString<IdentifiedItem>("""{"name":"Milk","confidence":0.9}""")
        }
    }

    @Test
    fun `missing confidence throws MissingFieldException`() {
        assertThrows(MissingFieldException::class.java) {
            json.decodeFromString<IdentifiedItem>("""{"name":"Milk","category":"dairy"}""")
        }
    }

    @Test
    fun `unknown keys are ignored, not rejected`() {
        val item = json.decodeFromString<IdentifiedItem>(
            """{"name":"Milk","category":"dairy","confidence":0.9,"quantity":2}""",
        )
        assertEquals("Milk", item.name)
    }

    @Test
    fun `category is not validated at decode time and maps through the enum`() {
        val item = json.decodeFromString<IdentifiedItem>(
            """{"name":"Milk","category":"Dairy","confidence":0.9}""",
        )
        assertEquals("Dairy", item.category)
        assertEquals(FoodCategory.DAIRY, item.foodCategory)

        val odd = json.decodeFromString<IdentifiedItem>(
            """{"name":"Sponge","category":"cleaning","confidence":0.4}""",
        )
        assertEquals(FoodCategory.OTHER, odd.foodCategory)
    }

    @Test
    fun `confidence is not range validated at decode time`() {
        val item = json.decodeFromString<IdentifiedItem>(
            """{"name":"Milk","category":"dairy","confidence":1.7}""",
        )
        assertEquals(1.7, item.confidence, 1e-9)
    }

    // MARK: - Recipe bundle

    @Test
    fun `recipe bundle decodes both buckets`() {
        val bundle = json.decodeFromString<RecipeBundleResponse>(
            """
            {"makeNow":[{"name":"Omelette","ingredients":[{"name":"Eggs","hasIt":true}],
            "steps":["Beat","Fry"],"timeMinutes":10,"missingItems":[]}],"almostThere":[]}
            """.trimIndent(),
        )
        assertEquals(1, bundle.makeNow.size)
        assertTrue(bundle.almostThere.isEmpty())
        val recipe = bundle.makeNow.first()
        assertEquals("Omelette", recipe.name)
        assertEquals(listOf(IngredientLine("Eggs", true)), recipe.ingredients)
        assertEquals(listOf("Beat", "Fry"), recipe.steps)
        assertEquals(10, recipe.timeMinutes)
        assertTrue(recipe.missingItems.isEmpty())
    }

    /**
     * `timeMinutes` is an `Int` in Swift, so a JSON float fails the WHOLE bundle.
     * Widening it here would accept a reply iOS rejects.
     */
    @Test
    fun `a float timeMinutes fails the whole bundle`() {
        assertThrows(Exception::class.java) {
            json.decodeFromString<RecipeBundleResponse>(
                """
                {"makeNow":[{"name":"Omelette","ingredients":[],"steps":[],
                "timeMinutes":10.5,"missingItems":[]}],"almostThere":[]}
                """.trimIndent(),
            )
        }
    }

    @Test
    fun `a missing recipe bucket fails the bundle`() {
        assertThrows(MissingFieldException::class.java) {
            json.decodeFromString<RecipeBundleResponse>("""{"makeNow":[]}""")
        }
    }

    @Test
    fun `a recipe missing a required field fails the bundle`() {
        assertThrows(MissingFieldException::class.java) {
            json.decodeFromString<RecipeBundleResponse>(
                """{"makeNow":[{"name":"Omelette","ingredients":[],"steps":[],"timeMinutes":10}],"almostThere":[]}""",
            )
        }
    }
}
