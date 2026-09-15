package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.graphics.Color

/**
 * The DinnerDecider Material 3 theme.
 *
 * ### There is deliberately no `dynamicColor` parameter
 * Material You would replace `primary`, `secondary` and `tertiary` with colors extracted
 * from the user's wallpaper, which is the entire brand gone — terracotta, sage and amber
 * are the app's identity, and on iOS they are fixed asset-catalog values with no
 * wallpaper-derived equivalent. Dynamic color is therefore not a toggle that defaults to
 * off; the parameter does not exist, so it cannot be switched on by a later screen task.
 * Two explicit schemes, light and dark, and nothing else.
 *
 * @param darkTheme follows the system setting by default. Pass explicitly in previews and
 *   screenshot tests only.
 */
@Composable
fun DinnerDeciderTheme(
    darkTheme: Boolean = isSystemInDarkTheme(),
    content: @Composable () -> Unit,
) {
    // Read the Reduce-Motion setting once here and hand it down, rather than letting every
    // animated element register its own ContentObserver. See LocalReduceMotion.
    CompositionLocalProvider(LocalReduceMotion provides observeSystemReduceMotion()) {
        MaterialTheme(
            colorScheme = if (darkTheme) DinnerDeciderDarkColors else DinnerDeciderLightColors,
            typography = DinnerDeciderTypography,
            shapes = DinnerDeciderShapes,
            content = content,
        )
    }
}

/**
 * iOS-parity color aliases.
 *
 * SwiftUI code says `Color.brandPrimary` / `Color.textSecondary`; Material 3 says
 * `colorScheme.primary` / `colorScheme.onSurfaceVariant`. These read-only aliases let the
 * fourteen screen ports keep the iOS vocabulary while still resolving through the single
 * [ColorScheme] that `MaterialTheme` already provides — no second source of truth, no
 * extra `CompositionLocal`, and light/dark switching stays automatic.
 *
 * Usage: `import com.philwoolley.dinnerdecider.designsystem.DinnerDeciderColors.brandPrimary`
 * then `MaterialTheme.colorScheme.brandPrimary`.
 */
object DinnerDeciderColors {
    /** Hero terracotta. iOS `Color.brandPrimary`. */
    val ColorScheme.brandPrimary: Color get() = primary

    /** Sage green. iOS `Color.brandSecondary`. */
    val ColorScheme.brandSecondary: Color get() = secondary

    /** Golden amber. iOS `Color.brandAccent`. */
    val ColorScheme.brandAccent: Color get() = tertiary

    /** App background — cream / charcoal. iOS `Color.surfacePrimary`. */
    val ColorScheme.surfacePrimary: Color get() = background

    /** Elevated surfaces (cards, rows). iOS `Color.surfaceSecondary`. */
    val ColorScheme.surfaceSecondary: Color get() = surface

    /** Primary reading text. iOS `Color.textPrimary`. */
    val ColorScheme.textPrimary: Color get() = onSurface

    /** Secondary text (captions, subtitles). iOS `Color.textSecondary`. */
    val ColorScheme.textSecondary: Color get() = onSurfaceVariant

    /** Separators, disabled states, hairlines. iOS `Color.separatorNeutral`. */
    val ColorScheme.separatorNeutral: Color get() = outlineVariant

    /**
     * Destructive red. iOS uses `Color.red` / `role: .destructive`; on Android the
     * platform's destructive red is the Material 3 error ramp. Never substitute
     * [brandPrimary] here — see the note in `Color.kt`.
     */
    val ColorScheme.destructive: Color get() = error
}
