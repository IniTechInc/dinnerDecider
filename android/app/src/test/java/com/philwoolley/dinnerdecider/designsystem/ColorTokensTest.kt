package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.material3.ColorScheme
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.toArgb
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.lang.reflect.Method

/**
 * Pins the brand palette to the **shipped iOS asset catalog**, not to the nominal hexes in
 * `CLAUDE.md`.
 *
 * Plan risk R7 is "brand color drift (LOW, high visibility)". The specific way it happens is
 * someone reading `CLAUDE.md` ("terracotta #E4573D, sage #7FA27A, amber #F2B23C"), noticing
 * the Kotlin values differ, and "fixing" them. Those nominal hues are the brand reference;
 * the values that actually ship were WCAG-tuned away from them in
 * `DinnerDecider/Assets.xcassets/<Token>.colorset/Contents.json`. This test is the tripwire, and
 * the expected values below were read out of those `Contents.json` files.
 */
class ColorTokensTest {

    private fun assertHex(expected: Long, actual: Color, label: String) {
        assertEquals(
            "$label expected #${expected.toString(16).uppercase().padStart(6, '0')} " +
                "but was #${(actual.toArgb().toLong() and 0xFFFFFF).toString(16).uppercase().padStart(6, '0')}",
            expected,
            actual.toArgb().toLong() and 0xFFFFFFL,
        )
    }

    // --- The eight shipped tokens -----------------------------------------

    @Test
    fun lightBrandTokensMatchTheAssetCatalog() {
        assertHex(0xC6442B, BrandPalette.BrandPrimaryLight, "BrandPrimary (light)")
        assertHex(0x557551, BrandPalette.BrandSecondaryLight, "BrandSecondary (light)")
        assertHex(0x8A6410, BrandPalette.BrandAccentLight, "BrandAccent (light)")
        assertHex(0xFFF6E8, BrandPalette.SurfacePrimaryLight, "SurfacePrimary (light)")
        assertHex(0xFAF8F5, BrandPalette.SurfaceSecondaryLight, "SurfaceSecondary (light)")
        assertHex(0x2B2724, BrandPalette.TextPrimaryLight, "TextPrimary (light)")
        assertHex(0x655D55, BrandPalette.TextSecondaryLight, "TextSecondary (light)")
        assertHex(0xCFC7BD, BrandPalette.SeparatorNeutralLight, "SeparatorNeutral (light)")
    }

    @Test
    fun darkBrandTokensMatchTheAssetCatalog() {
        assertHex(0xC6442B, BrandPalette.BrandPrimaryDark, "BrandPrimary (dark)")
        assertHex(0x8FB08A, BrandPalette.BrandSecondaryDark, "BrandSecondary (dark)")
        assertHex(0xEBB042, BrandPalette.BrandAccentDark, "BrandAccent (dark)")
        assertHex(0x1F1D1B, BrandPalette.SurfacePrimaryDark, "SurfacePrimary (dark)")
        assertHex(0x2A2724, BrandPalette.SurfaceSecondaryDark, "SurfaceSecondary (dark)")
        assertHex(0xF5EEE3, BrandPalette.TextPrimaryDark, "TextPrimary (dark)")
        assertHex(0xB8AEA2, BrandPalette.TextSecondaryDark, "TextSecondary (dark)")
        assertHex(0x4A453F, BrandPalette.SeparatorNeutralDark, "SeparatorNeutral (dark)")
    }

    @Test
    fun tokensAreNotTheNominalHexesFromClaudeMd() {
        // The exact "fix" this file exists to prevent.
        assertNotEquals(Color(0xFFE4573D), BrandPalette.BrandPrimaryLight)
        assertNotEquals(Color(0xFF7FA27A), BrandPalette.BrandSecondaryLight)
        assertNotEquals(Color(0xFFF2B23C), BrandPalette.BrandAccentLight)
    }

    @Test
    fun onlyBrandPrimaryIsSharedAcrossAppearances() {
        // The iOS color set ships BrandPrimary with an identical dark variant — that one is
        // meant to be the same. Every other token genuinely differs, and collapsing any of
        // them onto one hex is the drift this asserts against.
        assertEquals(BrandPalette.BrandPrimaryLight, BrandPalette.BrandPrimaryDark)
        assertNotEquals(BrandPalette.BrandSecondaryLight, BrandPalette.BrandSecondaryDark)
        assertNotEquals(BrandPalette.BrandAccentLight, BrandPalette.BrandAccentDark)
        assertNotEquals(BrandPalette.SurfacePrimaryLight, BrandPalette.SurfacePrimaryDark)
        assertNotEquals(BrandPalette.SurfaceSecondaryLight, BrandPalette.SurfaceSecondaryDark)
        assertNotEquals(BrandPalette.TextPrimaryLight, BrandPalette.TextPrimaryDark)
        assertNotEquals(BrandPalette.TextSecondaryLight, BrandPalette.TextSecondaryDark)
        assertNotEquals(BrandPalette.SeparatorNeutralLight, BrandPalette.SeparatorNeutralDark)
    }

    // --- Material 3 role mapping ------------------------------------------

    @Test
    fun lightSchemeMapsTheRequiredRoles() {
        with(DinnerDeciderLightColors) {
            assertEquals(BrandPalette.BrandPrimaryLight, primary)
            assertEquals(BrandPalette.BrandSecondaryLight, secondary)
            assertEquals(BrandPalette.BrandAccentLight, tertiary)
            assertEquals(BrandPalette.SurfacePrimaryLight, background)
            assertEquals(BrandPalette.SurfaceSecondaryLight, surface)
            assertEquals(BrandPalette.TextPrimaryLight, onBackground)
            assertEquals(BrandPalette.TextPrimaryLight, onSurface)
            assertEquals(BrandPalette.TextSecondaryLight, onSurfaceVariant)
            assertEquals(BrandPalette.SeparatorNeutralLight, outlineVariant)
        }
    }

    @Test
    fun darkSchemeMapsTheRequiredRoles() {
        with(DinnerDeciderDarkColors) {
            assertEquals(BrandPalette.BrandPrimaryDark, primary)
            assertEquals(BrandPalette.BrandSecondaryDark, secondary)
            assertEquals(BrandPalette.BrandAccentDark, tertiary)
            assertEquals(BrandPalette.SurfacePrimaryDark, background)
            assertEquals(BrandPalette.SurfaceSecondaryDark, surface)
            assertEquals(BrandPalette.TextPrimaryDark, onBackground)
            assertEquals(BrandPalette.TextPrimaryDark, onSurface)
            assertEquals(BrandPalette.TextSecondaryDark, onSurfaceVariant)
            assertEquals(BrandPalette.SeparatorNeutralDark, outlineVariant)
        }
    }

    @Test
    fun errorIsASystemRedAndNotDerivedFromTheBrand() {
        // "Destructive actions stay system red" (CLAUDE.md). Terracotta is close enough to a
        // red that reusing it for `error` would make a delete button indistinguishable from a
        // primary action.
        assertNotEquals(DinnerDeciderLightColors.primary, DinnerDeciderLightColors.error)
        assertNotEquals(DinnerDeciderDarkColors.primary, DinnerDeciderDarkColors.error)
        assertHex(0xB3261E, DinnerDeciderLightColors.error, "error (light)")
        assertHex(0xF2B8B5, DinnerDeciderDarkColors.error, "error (dark)")
    }

    // --- No Material You leakage ------------------------------------------

    /**
     * Every `Color`-returning role on [ColorScheme], by name.
     *
     * `Color` is an inline value class over `ULong`, so its getters compile to `long`-returning
     * methods with a mangled `-<hash>` suffix. The hash is not stable across Compose versions,
     * so the suffix is stripped rather than matched.
     */
    private fun roles(scheme: ColorScheme): List<Pair<String, Color>> {
        val getters: List<Method> = ColorScheme::class.java.methods
            .filter { it.parameterCount == 0 && it.returnType == Long::class.javaPrimitiveType }
            .filter { it.name.startsWith("get") }
            .sortedBy { it.name }
        return getters.map { method ->
            val name = method.name.removePrefix("get").substringBefore('-')
                .replaceFirstChar { it.lowercase() }
            name to Color((method.invoke(scheme) as Long).toULong())
        }
    }

    @Test
    fun noRoleIsLeftOnTheMaterialBaselinePurple() {
        // lightColorScheme()/darkColorScheme() silently fill anything unspecified with M3's
        // baseline violet ramp. A single unspecified role puts purple on a Card or a Switch.
        // Heuristic: in this warm palette the blue channel never exceeds the red channel by
        // more than a hair, whereas every baseline-purple role is strongly blue-dominant.
        listOf("light" to DinnerDeciderLightColors, "dark" to DinnerDeciderDarkColors)
            .forEach { (label, scheme) ->
                val found = roles(scheme)
                assertTrue("Reflection found no ColorScheme roles for $label.", found.size >= 25)
                val violet = found.filter { (name, color) ->
                    name != "scrim" && color.blue - color.red > 0.06f
                }
                assertTrue(
                    "These $label roles look like the Material baseline purple rather than a " +
                        "DinnerDecider warm neutral: ${violet.map { it.first }}. Pin them in Color.kt.",
                    violet.isEmpty(),
                )
            }
    }

    @Test
    fun thereIsNoDynamicColorEntryPoint() {
        // Material You would replace primary/secondary/tertiary with wallpaper-derived hues,
        // i.e. delete the brand. The guard is structural rather than documentary:
        // DinnerDeciderTheme has no dynamicColor parameter at all, so a screen task cannot
        // switch it on. Parameter *names* are erased unless the module is compiled with
        // -parameters, so this asserts on the shape instead: exactly one boolean (darkTheme).
        // Adding `dynamicColor: Boolean` would make it two, and this fails.
        val clazz = Class.forName("com.philwoolley.dinnerdecider.designsystem.ThemeKt")
        val method = clazz.methods.first { it.name == "DinnerDeciderTheme" }
        val booleanParams = method.parameterTypes.count { it == Boolean::class.javaPrimitiveType }
        assertEquals(
            "DinnerDeciderTheme should take exactly one boolean (darkTheme). A second boolean " +
                "is almost certainly a dynamicColor switch, which would discard the brand palette.",
            1,
            booleanParams,
        )
    }
}
