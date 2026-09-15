package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.ui.graphics.Color

/**
 * DinnerDecider brand color tokens, ported from the **shipped iOS asset catalog**.
 *
 * ### Why these values and not the ones in CLAUDE.md
 * `CLAUDE.md` names the brand palette nominally (terracotta `#E4573D`, sage `#7FA27A`,
 * amber `#F2B23C`). Those are the *reference* hues. The colors that actually ship were
 * WCAG-tuned away from them in `DinnerDecider/Assets.xcassets/<Token>.colorset/Contents.json`
 * so body text clears 4.5:1 and UI glyphs clear 3:1 in both appearances. "Match iOS"
 * means matching what a user sees on screen, so the asset-catalog values are the source
 * of truth and are reproduced verbatim below. Changing any of them to the nominal hex is
 * a visible parity regression (plan risk R7) — verified by `ColorTokensTest`.
 *
 * | Token | Light | Dark |
 * |---|---|---|
 * | BrandPrimary | `#C6442B` | `#C6442B` (identical by design) |
 * | BrandSecondary | `#557551` | `#8FB08A` |
 * | BrandAccent | `#8A6410` | `#EBB042` |
 * | SurfacePrimary | `#FFF6E8` | `#1F1D1B` |
 * | SurfaceSecondary | `#FAF8F5` | `#2A2724` |
 * | TextPrimary | `#2B2724` | `#F5EEE3` |
 * | TextSecondary | `#655D55` | `#B8AEA2` |
 * | SeparatorNeutral | `#CFC7BD` | `#4A453F` |
 *
 * Note that **BrandPrimary is deliberately the same hex in both appearances** — the iOS
 * color set carries an explicit dark variant with identical components. Every other brand
 * token genuinely differs; do not collapse them onto one value.
 */
object BrandPalette {

    // --- Light -------------------------------------------------------------

    /** Hero terracotta. Buttons, key actions, brand moments, primary tint. */
    val BrandPrimaryLight = Color(0xFFC6442B)

    /** Sage green. Fresh/healthy accents, on-device Gemma badge, confident scores. */
    val BrandSecondaryLight = Color(0xFF557551)

    /** Golden amber. Highlights, "almost there", low-confidence caution, demo badge. */
    val BrandAccentLight = Color(0xFF8A6410)

    /** App background. Warm cream. */
    val SurfacePrimaryLight = Color(0xFFFFF6E8)

    /** Elevated surfaces (cards, rows). Soft off-white. */
    val SurfaceSecondaryLight = Color(0xFFFAF8F5)

    /** Primary reading text. Warm near-black. */
    val TextPrimaryLight = Color(0xFF2B2724)

    /** Secondary text (captions, subtitles). Warm greige, at least 4.5:1 on cream. */
    val TextSecondaryLight = Color(0xFF655D55)

    /** Separators, disabled states, tertiary hairlines. Warm greige. */
    val SeparatorNeutralLight = Color(0xFFCFC7BD)

    // --- Dark --------------------------------------------------------------

    /** Hero terracotta — the iOS color set ships the identical hex in dark. */
    val BrandPrimaryDark = Color(0xFFC6442B)

    /** Sage green, lifted for legibility on charcoal. */
    val BrandSecondaryDark = Color(0xFF8FB08A)

    /** Golden amber, lifted for legibility on charcoal. */
    val BrandAccentDark = Color(0xFFEBB042)

    /** App background. Warm charcoal. */
    val SurfacePrimaryDark = Color(0xFF1F1D1B)

    /** Elevated surfaces. Lifted charcoal. */
    val SurfaceSecondaryDark = Color(0xFF2A2724)

    /** Primary reading text. Warm near-white. */
    val TextPrimaryDark = Color(0xFFF5EEE3)

    /** Secondary text. Warm greige. */
    val TextSecondaryDark = Color(0xFFB8AEA2)

    /** Separators and hairlines on charcoal. */
    val SeparatorNeutralDark = Color(0xFF4A453F)
}

/**
 * Supporting neutrals and container tints.
 *
 * These are *not* iOS tokens — iOS gets them free from UIKit's system materials. Material 3
 * has roughly thirty color roles and [lightColorScheme]/[darkColorScheme] fill anything left
 * unspecified with the M3 **baseline purple**. Leaving them unspecified would leak violet into
 * every Card, NavigationBar, Chip and Switch in the app, which is exactly the brand drift this
 * file exists to prevent. So each remaining role is pinned to a warm neutral or a brand tint
 * derived from the eight shipped tokens above.
 */
private object SupportColors {
    val White = Color(0xFFFFFFFF)
    val Black = Color(0xFF000000)

    // Light containers / neutrals.
    val PrimaryContainerLight = Color(0xFFFFDAD3)
    val OnPrimaryContainerLight = Color(0xFF410000)
    val SecondaryContainerLight = Color(0xFFD5EACF)
    val OnSecondaryContainerLight = Color(0xFF122111)
    val TertiaryContainerLight = Color(0xFFFFE1A6)
    val OnTertiaryContainerLight = Color(0xFF2B1D00)
    val SurfaceVariantLight = Color(0xFFF1EADF)
    val SurfaceDimLight = Color(0xFFEFE7DA)
    val SurfaceBrightLight = Color(0xFFFFF6E8)
    val SurfaceContainerLowestLight = Color(0xFFFFFFFF)
    val SurfaceContainerLowLight = Color(0xFFFDFAF4)
    val SurfaceContainerLight = Color(0xFFFAF8F5)
    val SurfaceContainerHighLight = Color(0xFFF4EFE6)
    val SurfaceContainerHighestLight = Color(0xFFEEE8DC)
    val OutlineLight = Color(0xFF7C736A)
    val InverseSurfaceLight = Color(0xFF34302C)
    val InverseOnSurfaceLight = Color(0xFFF5EEE3)
    val InversePrimaryLight = Color(0xFFFFB4A4)

    // Dark containers / neutrals.
    val PrimaryContainerDark = Color(0xFF8E2C18)
    val OnPrimaryContainerDark = Color(0xFFFFDAD3)
    val SecondaryContainerDark = Color(0xFF3D5539)
    val OnSecondaryContainerDark = Color(0xFFD5EACF)
    val TertiaryContainerDark = Color(0xFF5E4508)
    val OnTertiaryContainerDark = Color(0xFFFFE1A6)
    val SurfaceVariantDark = Color(0xFF3A3532)
    val SurfaceDimDark = Color(0xFF1A1817)
    val SurfaceBrightDark = Color(0xFF413C38)
    val SurfaceContainerLowestDark = Color(0xFF171514)
    val SurfaceContainerLowDark = Color(0xFF221F1D)
    val SurfaceContainerDark = Color(0xFF2A2724)
    val SurfaceContainerHighDark = Color(0xFF34302C)
    val SurfaceContainerHighestDark = Color(0xFF3F3A35)
    val OutlineDark = Color(0xFF8C8378)
    val InverseSurfaceDark = Color(0xFFF5EEE3)
    val InverseOnSurfaceDark = Color(0xFF2B2724)
    val InversePrimaryDark = Color(0xFFC6442B)
}

/**
 * Destructive red.
 *
 * `CLAUDE.md`: *"Destructive actions stay system red."* On iOS that is literally `Color.red`.
 * The Android analogue of "the platform's own destructive red" is the Material 3 baseline
 * error ramp, which every Compose component already reaches for via
 * `MaterialTheme.colorScheme.error`. It is deliberately **not** derived from BrandPrimary:
 * terracotta and a destructive red must never be confusable, and `#C6442B` is close enough
 * to a red that a brand-derived error color would blur exactly that distinction.
 */
private object ErrorColors {
    val ErrorLight = Color(0xFFB3261E)
    val OnErrorLight = Color(0xFFFFFFFF)
    val ErrorContainerLight = Color(0xFFF9DEDC)
    val OnErrorContainerLight = Color(0xFF410E0B)

    val ErrorDark = Color(0xFFF2B8B5)
    val OnErrorDark = Color(0xFF601410)
    val ErrorContainerDark = Color(0xFF8C1D18)
    val OnErrorContainerDark = Color(0xFFF9DEDC)
}

/**
 * Light Material 3 scheme.
 *
 * Required mapping (plan Step 22):
 * `primary` = BrandPrimary, `secondary` = BrandSecondary, `tertiary` = BrandAccent,
 * `background` = SurfacePrimary, `surface` = SurfaceSecondary,
 * `onBackground`/`onSurface` = TextPrimary, `onSurfaceVariant` = TextSecondary,
 * `outlineVariant` = SeparatorNeutral, `error` = system red.
 */
val DinnerDeciderLightColors: ColorScheme = lightColorScheme(
    primary = BrandPalette.BrandPrimaryLight,
    onPrimary = SupportColors.White,
    primaryContainer = SupportColors.PrimaryContainerLight,
    onPrimaryContainer = SupportColors.OnPrimaryContainerLight,
    inversePrimary = SupportColors.InversePrimaryLight,

    secondary = BrandPalette.BrandSecondaryLight,
    onSecondary = SupportColors.White,
    secondaryContainer = SupportColors.SecondaryContainerLight,
    onSecondaryContainer = SupportColors.OnSecondaryContainerLight,

    tertiary = BrandPalette.BrandAccentLight,
    onTertiary = SupportColors.White,
    tertiaryContainer = SupportColors.TertiaryContainerLight,
    onTertiaryContainer = SupportColors.OnTertiaryContainerLight,

    background = BrandPalette.SurfacePrimaryLight,
    onBackground = BrandPalette.TextPrimaryLight,

    surface = BrandPalette.SurfaceSecondaryLight,
    onSurface = BrandPalette.TextPrimaryLight,
    surfaceVariant = SupportColors.SurfaceVariantLight,
    onSurfaceVariant = BrandPalette.TextSecondaryLight,
    surfaceTint = BrandPalette.BrandPrimaryLight,
    inverseSurface = SupportColors.InverseSurfaceLight,
    inverseOnSurface = SupportColors.InverseOnSurfaceLight,
    surfaceDim = SupportColors.SurfaceDimLight,
    surfaceBright = SupportColors.SurfaceBrightLight,
    surfaceContainerLowest = SupportColors.SurfaceContainerLowestLight,
    surfaceContainerLow = SupportColors.SurfaceContainerLowLight,
    surfaceContainer = SupportColors.SurfaceContainerLight,
    surfaceContainerHigh = SupportColors.SurfaceContainerHighLight,
    surfaceContainerHighest = SupportColors.SurfaceContainerHighestLight,

    error = ErrorColors.ErrorLight,
    onError = ErrorColors.OnErrorLight,
    errorContainer = ErrorColors.ErrorContainerLight,
    onErrorContainer = ErrorColors.OnErrorContainerLight,

    outline = SupportColors.OutlineLight,
    outlineVariant = BrandPalette.SeparatorNeutralLight,
    scrim = SupportColors.Black,
)

/** Dark Material 3 scheme. Same role mapping as [DinnerDeciderLightColors]. */
val DinnerDeciderDarkColors: ColorScheme = darkColorScheme(
    primary = BrandPalette.BrandPrimaryDark,
    onPrimary = SupportColors.White,
    primaryContainer = SupportColors.PrimaryContainerDark,
    onPrimaryContainer = SupportColors.OnPrimaryContainerDark,
    inversePrimary = SupportColors.InversePrimaryDark,

    secondary = BrandPalette.BrandSecondaryDark,
    // Sage at #8FB08A is light enough that white on it fails 4.5:1; charcoal clears 8:1.
    onSecondary = BrandPalette.SurfacePrimaryDark,
    secondaryContainer = SupportColors.SecondaryContainerDark,
    onSecondaryContainer = SupportColors.OnSecondaryContainerDark,

    tertiary = BrandPalette.BrandAccentDark,
    // Same reasoning as onSecondary: #EBB042 is a bright amber, so text on it must be dark.
    onTertiary = BrandPalette.SurfacePrimaryDark,
    tertiaryContainer = SupportColors.TertiaryContainerDark,
    onTertiaryContainer = SupportColors.OnTertiaryContainerDark,

    background = BrandPalette.SurfacePrimaryDark,
    onBackground = BrandPalette.TextPrimaryDark,

    surface = BrandPalette.SurfaceSecondaryDark,
    onSurface = BrandPalette.TextPrimaryDark,
    surfaceVariant = SupportColors.SurfaceVariantDark,
    onSurfaceVariant = BrandPalette.TextSecondaryDark,
    surfaceTint = BrandPalette.BrandPrimaryDark,
    inverseSurface = SupportColors.InverseSurfaceDark,
    inverseOnSurface = SupportColors.InverseOnSurfaceDark,
    surfaceDim = SupportColors.SurfaceDimDark,
    surfaceBright = SupportColors.SurfaceBrightDark,
    surfaceContainerLowest = SupportColors.SurfaceContainerLowestDark,
    surfaceContainerLow = SupportColors.SurfaceContainerLowDark,
    surfaceContainer = SupportColors.SurfaceContainerDark,
    surfaceContainerHigh = SupportColors.SurfaceContainerHighDark,
    surfaceContainerHighest = SupportColors.SurfaceContainerHighestDark,

    error = ErrorColors.ErrorDark,
    onError = ErrorColors.OnErrorDark,
    errorContainer = ErrorColors.ErrorContainerDark,
    onErrorContainer = ErrorColors.OnErrorContainerDark,

    outline = SupportColors.OutlineDark,
    outlineVariant = BrandPalette.SeparatorNeutralDark,
    scrim = SupportColors.Black,
)
