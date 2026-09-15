package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import com.philwoolley.dinnerdecider.R

/**
 * DM Serif Display — the brand display face.
 *
 * ### Rule: never below 22 sp. This is not a style preference.
 * DM Serif Display ships a **single weight** (Regular, plus an Italic). It has no bold, no
 * medium, and no optical size for small text, so at body sizes it loses stroke contrast,
 * the thin serifs disappear on low-density screens, and synthetic bolding smears it. iOS
 * encodes the same rule as "never use DM Serif Display below the `.title2` scale"
 * (`DinnerDecider/DesignSystem/Theme.swift`), and `.title2` is 22 pt.
 *
 * The rule is **enforced, not merely documented**: `TypographyTest` walks every property of
 * [DinnerDeciderTypography] by reflection and fails if any style using this family is under
 * 22 sp. Adding a serif style at 20 sp breaks the build, which is the point — a documented
 * convention with fourteen screen tasks running in parallel is a convention that will be
 * broken by accident.
 *
 * Only three slots use it: [Typography.displayLarge] (34 sp), [Typography.displayMedium]
 * (28 sp) and [Typography.headlineSmall] (22 sp, the smallest permitted serif).
 */
val DmSerifDisplay: FontFamily = FontFamily(
    Font(R.font.dm_serif_display_regular, FontWeight.Normal, FontStyle.Normal),
    Font(R.font.dm_serif_display_italic, FontWeight.Normal, FontStyle.Italic),
)

/**
 * DM Sans — every functional and body surface in the app.
 *
 * Four real weights are bundled (400/500/600/700) so Compose never has to synthesize one.
 * Sizes are held to the iOS ladder: 17 / 16 / 15 / 13 / 12 sp, mirroring the Dynamic Type
 * tokens `dmBody`, `dmCallout`, `dmSubheadline`, `dmFootnote`, `dmCaption`.
 */
val DmSans: FontFamily = FontFamily(
    Font(R.font.dm_sans_regular, FontWeight.Normal),
    Font(R.font.dm_sans_medium, FontWeight.Medium),
    Font(R.font.dm_sans_semibold, FontWeight.SemiBold),
    Font(R.font.dm_sans_bold, FontWeight.Bold),
)

/**
 * The DinnerDecider Material 3 type scale.
 *
 * ### How the iOS tokens map onto M3's fifteen slots
 *
 * | M3 slot | Face | Size | iOS token |
 * |---|---|---|---|
 * | `displayLarge` | **Serif** | 34 sp | `displayLarge` |
 * | `displayMedium` | **Serif** | 28 sp | `displayTitle` |
 * | `headlineSmall` | **Serif** | 22 sp | `recipeName` |
 * | `titleLarge` | Sans Bold | 17 sp | `dmBodyBold` |
 * | `titleMedium` | Sans SemiBold | 17 sp | `dmHeadline` |
 * | `titleSmall` | Sans SemiBold | 15 sp | `dmSectionHeader` |
 * | `bodyLarge` | Sans Regular | 17 sp | `dmBody` |
 * | `bodyMedium` | Sans Regular | 16 sp | `dmCallout` |
 * | `bodySmall` | Sans Regular | 15 sp | `dmSubheadline` |
 * | `labelLarge` | Sans SemiBold | 16 sp | button labels |
 * | `labelMedium` | Sans Medium | 13 sp | `dmFootnote` / `dmCaptionMedium` |
 * | `labelSmall` | Sans Regular | 12 sp | `dmCaption` |
 *
 * ### The three leftover slots, and why they look "out of order"
 * `displaySmall`, `headlineLarge` and `headlineMedium` have **no iOS counterpart** —
 * DinnerDecider's scale is one serif display face plus one compact sans ladder, and that is
 * eleven tokens, not fifteen. They are still filled in, because any slot left unspecified
 * falls back to M3's baseline Roboto and would put a system font on screen next to DM Sans.
 * They are mapped to the nearest sans token instead, which makes the M3 *headline* tier
 * numerically smaller than `headlineSmall` (17/16 sp vs 22 sp). That inversion is
 * deliberate and harmless: screens address tokens by name, and the three unmapped slots are
 * not part of the design language. **Prefer the named aliases below** over raw M3 slots.
 */
val DinnerDeciderTypography: Typography = Typography(
    // --- Serif display: 34 / 28 / 22 sp, and nothing smaller ---------------
    displayLarge = TextStyle(
        fontFamily = DmSerifDisplay,
        fontWeight = FontWeight.Normal,
        fontSize = 34.sp,
        lineHeight = 41.sp,
        letterSpacing = 0.sp,
    ),
    displayMedium = TextStyle(
        fontFamily = DmSerifDisplay,
        fontWeight = FontWeight.Normal,
        fontSize = 28.sp,
        lineHeight = 34.sp,
        letterSpacing = 0.sp,
    ),
    headlineSmall = TextStyle(
        fontFamily = DmSerifDisplay,
        fontWeight = FontWeight.Normal,
        fontSize = 22.sp,
        lineHeight = 28.sp,
        letterSpacing = 0.sp,
    ),

    // --- Unmapped M3 slots, pinned to sans so nothing falls back to Roboto --
    displaySmall = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Bold,
        fontSize = 17.sp,
        lineHeight = 24.sp,
        letterSpacing = 0.sp,
    ),
    headlineLarge = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Bold,
        fontSize = 17.sp,
        lineHeight = 24.sp,
        letterSpacing = 0.sp,
    ),
    headlineMedium = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.SemiBold,
        fontSize = 16.sp,
        lineHeight = 22.sp,
        letterSpacing = 0.sp,
    ),

    // --- Titles -----------------------------------------------------------
    titleLarge = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Bold,
        fontSize = 17.sp,
        lineHeight = 24.sp,
        letterSpacing = 0.sp,
    ),
    titleMedium = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.SemiBold,
        fontSize = 17.sp,
        lineHeight = 24.sp,
        letterSpacing = 0.sp,
    ),
    titleSmall = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.SemiBold,
        fontSize = 15.sp,
        lineHeight = 20.sp,
        letterSpacing = 0.1.sp,
    ),

    // --- Body -------------------------------------------------------------
    bodyLarge = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Normal,
        fontSize = 17.sp,
        lineHeight = 24.sp,
        letterSpacing = 0.sp,
    ),
    bodyMedium = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Normal,
        fontSize = 16.sp,
        lineHeight = 22.sp,
        letterSpacing = 0.sp,
    ),
    bodySmall = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Normal,
        fontSize = 15.sp,
        lineHeight = 20.sp,
        letterSpacing = 0.sp,
    ),

    // --- Labels -----------------------------------------------------------
    labelLarge = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.SemiBold,
        fontSize = 16.sp,
        lineHeight = 20.sp,
        letterSpacing = 0.sp,
    ),
    labelMedium = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Medium,
        fontSize = 13.sp,
        lineHeight = 18.sp,
        letterSpacing = 0.sp,
    ),
    labelSmall = TextStyle(
        fontFamily = DmSans,
        fontWeight = FontWeight.Normal,
        fontSize = 12.sp,
        lineHeight = 16.sp,
        letterSpacing = 0.sp,
    ),
)

/**
 * iOS-parity aliases.
 *
 * Screen code should reach for these rather than remembering that "recipe name" is spelled
 * `headlineSmall` in Material 3. They also keep the port mechanical: a SwiftUI view using
 * `.font(.dmSectionHeader)` becomes `style = MaterialTheme.typography.dmSectionHeader`.
 */
object DinnerDeciderType {
    /** Largest brand/display text (onboarding headlines). DM Serif Display 34 sp. */
    val Typography.displayHero: TextStyle get() = displayLarge

    /** Display title for prominent headers. DM Serif Display 28 sp. */
    val Typography.displayTitle: TextStyle get() = displayMedium

    /** Recipe names and card titles. DM Serif Display 22 sp — the smallest allowed serif. */
    val Typography.recipeName: TextStyle get() = headlineSmall

    /** Standard body copy. DM Sans 17 sp. */
    val Typography.dmBody: TextStyle get() = bodyLarge

    /** Callout / secondary body. DM Sans 16 sp. */
    val Typography.dmCallout: TextStyle get() = bodyMedium

    /** Subheadline. DM Sans 15 sp. */
    val Typography.dmSubheadline: TextStyle get() = bodySmall

    /** Footnote. DM Sans Medium 13 sp. */
    val Typography.dmFootnote: TextStyle get() = labelMedium

    /** Caption. DM Sans 12 sp. */
    val Typography.dmCaption: TextStyle get() = labelSmall

    /** Emphasised body / inline headline. DM Sans SemiBold 17 sp. */
    val Typography.dmHeadline: TextStyle get() = titleMedium

    /** Section headers and list titles. DM Sans SemiBold 15 sp. */
    val Typography.dmSectionHeader: TextStyle get() = titleSmall

    /** Strong emphasis / prominent button labels. DM Sans Bold 17 sp. */
    val Typography.dmBodyBold: TextStyle get() = titleLarge
}
