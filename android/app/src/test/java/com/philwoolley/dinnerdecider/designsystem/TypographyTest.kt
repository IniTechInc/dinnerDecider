package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.TextUnitType
import androidx.compose.ui.unit.sp
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.lang.reflect.Method

/**
 * Enforces the design-system rules that plan Step 22 states in prose.
 *
 * The load-bearing one is [serifIsNeverSmallerThanTitle2]: DM Serif Display ships a single
 * weight and reads badly below the iOS `.title2` scale (22 pt), so "never serif below 22 sp"
 * is a rule the codebase must be unable to break by accident. It is checked by **walking
 * every style on the [Typography] object reflectively** rather than by listing the three
 * known serif slots — a future style added at 18 sp with the serif family is caught by the
 * walk and would be invisible to a hand-written list.
 *
 * Runs on the host JVM: constructing `TextStyle`/`Typography`/`FontFamily` only builds data
 * classes, and `Font(resId)` is a lazy `ResourceFont` descriptor that resolves nothing until
 * composition. No Robolectric, no device.
 */
class TypographyTest {

    /** The floor, in sp. iOS `.title2` is 22 pt; DM Serif Display is unusable below it. */
    private val serifMinimumSp = 22f

    /** Every `TextStyle`-returning getter on [Typography], paired with its property name. */
    private fun allTextStyles(): List<Pair<String, TextStyle>> {
        val getters: List<Method> = Typography::class.java.methods
            .filter { it.parameterCount == 0 && it.returnType == TextStyle::class.java }
            .filter { it.name.startsWith("get") }
            .sortedBy { it.name }
        return getters.map { method ->
            val name = method.name.removePrefix("get").replaceFirstChar { it.lowercase() }
            name to (method.invoke(DinnerDeciderTypography) as TextStyle)
        }
    }

    @Test
    fun reflectiveWalkFindsTheFullMaterial3Scale() {
        // If Material 3 ever grows or renames a slot this number moves, and the failure is a
        // prompt to re-check the mapping table in Type.kt rather than a silent gap in coverage.
        assertEquals(
            "Expected the 15 Material 3 type-scale slots; got ${allTextStyles().map { it.first }}",
            15,
            allTextStyles().size,
        )
    }

    @Test
    fun serifIsNeverSmallerThanTitle2() {
        val offenders = allTextStyles()
            .filter { (_, style) -> style.fontFamily == DmSerifDisplay }
            .filter { (_, style) -> style.fontSize.value < serifMinimumSp }
            .map { (name, style) -> "$name = ${style.fontSize.value}sp" }

        assertTrue(
            "DM Serif Display must never be used below ${serifMinimumSp.toInt()}sp " +
                "(it is a single-weight display face and loses its serifs at small sizes). " +
                "Offending styles: $offenders. Use a DM Sans token instead.",
            offenders.isEmpty(),
        )
    }

    @Test
    fun serifIsUsedByExactlyTheThreeDisplaySlots() {
        val serifSlots = allTextStyles()
            .filter { (_, style) -> style.fontFamily == DmSerifDisplay }
            .map { it.first }
            .sorted()

        assertEquals(
            listOf("displayLarge", "displayMedium", "headlineSmall"),
            serifSlots,
        )
    }

    @Test
    fun serifSizesMatchIos() {
        assertEquals(34f, DinnerDeciderTypography.displayLarge.fontSize.value, 0f)
        assertEquals(28f, DinnerDeciderTypography.displayMedium.fontSize.value, 0f)
        assertEquals(22f, DinnerDeciderTypography.headlineSmall.fontSize.value, 0f)
    }

    @Test
    fun everyStyleDeclaresABundledFamilySoNothingFallsBackToRoboto() {
        allTextStyles().forEach { (name, style) ->
            assertNotNull("$name has no fontFamily; it would render in Roboto.", style.fontFamily)
            assertTrue(
                "$name uses ${style.fontFamily}, which is neither DM Sans nor DM Serif Display.",
                style.fontFamily == DmSans || style.fontFamily == DmSerifDisplay,
            )
        }
    }

    @Test
    fun everyStyleIsSizedInScalableSpNotFixedPixels() {
        allTextStyles().forEach { (name, style) ->
            assertEquals(
                "$name must be sized in sp so it honours the user's font-size setting.",
                TextUnitType.Sp,
                style.fontSize.type,
            )
        }
    }

    @Test
    fun sansSizesFollowTheIosLadder() {
        // 17 / 16 / 15 / 13 / 12 — the five Dynamic Type sizes iOS ships. Any other size on a
        // DM Sans style means someone reached past the scale.
        val allowed = setOf(17f, 16f, 15f, 13f, 12f)
        val offenders = allTextStyles()
            .filter { (_, style) -> style.fontFamily == DmSans }
            .filterNot { (_, style) -> style.fontSize.value in allowed }
            .map { (name, style) -> "$name = ${style.fontSize.value}sp" }

        assertTrue("DM Sans styles off the 17/16/15/13/12 ladder: $offenders", offenders.isEmpty())
    }

    @Test
    fun namedSansSlotsMatchIosTokens() {
        with(DinnerDeciderTypography) {
            // dmBody / dmCallout / dmSubheadline
            assertEquals(17f, bodyLarge.fontSize.value, 0f)
            assertEquals(16f, bodyMedium.fontSize.value, 0f)
            assertEquals(15f, bodySmall.fontSize.value, 0f)
            // dmHeadline / dmSectionHeader / dmBodyBold
            assertEquals(17f, titleMedium.fontSize.value, 0f)
            assertEquals(FontWeight.SemiBold, titleMedium.fontWeight)
            assertEquals(15f, titleSmall.fontSize.value, 0f)
            assertEquals(FontWeight.SemiBold, titleSmall.fontWeight)
            assertEquals(17f, titleLarge.fontSize.value, 0f)
            assertEquals(FontWeight.Bold, titleLarge.fontWeight)
            // dmFootnote / dmCaption
            assertEquals(13f, labelMedium.fontSize.value, 0f)
            assertEquals(FontWeight.Medium, labelMedium.fontWeight)
            assertEquals(12f, labelSmall.fontSize.value, 0f)
        }
    }

    @Test
    fun everyStyleHasALineHeightAtLeastAsLargeAsItsFontSize() {
        allTextStyles().forEach { (name, style) ->
            assertFalse("$name has no lineHeight.", style.lineHeight == androidx.compose.ui.unit.TextUnit.Unspecified)
            assertTrue(
                "$name has lineHeight ${style.lineHeight.value}sp below its ${style.fontSize.value}sp size.",
                style.lineHeight.value >= style.fontSize.value,
            )
        }
    }

    @Test
    fun bothFamiliesBundleEveryFaceSoNothingIsSynthesised() {
        // Four real DM Sans weights (400/500/600/700) and two DM Serif Display faces
        // (roman + italic) ship under res/font, so Compose never has to fake a bold or a
        // slant. FontListFontFamily delegates to List<Font>, hence the size check.
        assertEquals(4, (DmSans as List<*>).size)
        assertEquals(2, (DmSerifDisplay as List<*>).size)
        assertFalse("The two families must be distinct.", DmSans == DmSerifDisplay)
    }

    @Test
    fun typographyIsASingletonSoScreensCannotForkTheScale() {
        // Guards against a screen task building its own Typography: the object identity here is
        // what DinnerDeciderTheme installs.
        assertTrue(DinnerDeciderTypography === DinnerDeciderTypography)
        assertEquals(0.sp, DinnerDeciderTypography.bodyLarge.letterSpacing)
    }
}
