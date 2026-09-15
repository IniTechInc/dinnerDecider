package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Shapes
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * Corner radius scale, ported 1:1 from iOS `Radius` in
 * `DinnerDecider/DesignSystem/Theme.swift`.
 *
 * The brand direction is warm and organic, so containers use generous 16–20 dp radii.
 * iOS asks call sites for `RoundedRectangle(cornerRadius:style:.continuous)` (a squircle);
 * Compose has no continuous-corner primitive, so [RoundedCornerShape] is the closest
 * equivalent and is what [DinnerDeciderShapes] uses. That is a deliberate, visible
 * approximation rather than a missing feature — do not hand-roll a squircle path for it.
 */
object Radius {
    /** 8 dp. Chips, small controls. */
    val small: Dp = 8.dp

    /** 12 dp. Compact containers. */
    val medium: Dp = 12.dp

    /** 16 dp. Cards, banners, thumbnails — the default card radius. */
    val card: Dp = 16.dp

    /** 20 dp. Large hero cards. */
    val large: Dp = 20.dp
}

/**
 * Spacing scale (multiples of 4), ported 1:1 from iOS `Spacing`.
 *
 * Use these instead of literal dp values so vertical rhythm stays consistent as fourteen
 * screen tasks land in parallel.
 */
object Spacing {
    /** 4 dp. Hairline gaps, icon-to-label padding. */
    val xs: Dp = 4.dp

    /** 8 dp. Tight stacks. */
    val sm: Dp = 8.dp

    /** 12 dp. Row internals. */
    val md: Dp = 12.dp

    /** 16 dp. Default screen padding and section gaps. */
    val lg: Dp = 16.dp

    /** 24 dp. Major section separation, horizontal insets on centred copy. */
    val xl: Dp = 24.dp
}

/**
 * Material 3 shape roles wired to the [Radius] scale.
 *
 * M3 has five roles against DinnerDecider's four radii, so `extraSmall` also takes
 * [Radius.small]: the iOS scale simply has nothing below 8 dp, and letting `extraSmall`
 * keep its 4 dp M3 default would put an off-scale radius on small components (badges,
 * tooltips) that sit right next to 8 dp chips.
 */
val DinnerDeciderShapes: Shapes = Shapes(
    extraSmall = RoundedCornerShape(Radius.small),
    small = RoundedCornerShape(Radius.small),
    medium = RoundedCornerShape(Radius.medium),
    large = RoundedCornerShape(Radius.card),
    extraLarge = RoundedCornerShape(Radius.large),
)
