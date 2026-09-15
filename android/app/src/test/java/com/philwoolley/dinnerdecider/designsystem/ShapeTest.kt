package com.philwoolley.dinnerdecider.designsystem

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.unit.dp
import org.junit.Assert.assertEquals
import org.junit.Test

/** Pins the radius and spacing scales to their iOS counterparts in `Theme.swift`. */
class ShapeTest {

    @Test
    fun radiusScaleMatchesIos() {
        assertEquals(8.dp, Radius.small)
        assertEquals(12.dp, Radius.medium)
        assertEquals(16.dp, Radius.card)
        assertEquals(20.dp, Radius.large)
    }

    @Test
    fun spacingScaleMatchesIos() {
        assertEquals(4.dp, Spacing.xs)
        assertEquals(8.dp, Spacing.sm)
        assertEquals(12.dp, Spacing.md)
        assertEquals(16.dp, Spacing.lg)
        assertEquals(24.dp, Spacing.xl)
    }

    @Test
    fun spacingIsAlwaysAMultipleOfFour() {
        listOf(Spacing.xs, Spacing.sm, Spacing.md, Spacing.lg, Spacing.xl).forEach {
            assertEquals("${it.value}dp is off the 4dp grid.", 0f, it.value % 4f, 0f)
        }
    }

    @Test
    fun materialShapeRolesUseTheRadiusScale() {
        assertEquals(RoundedCornerShape(Radius.small), DinnerDeciderShapes.extraSmall)
        assertEquals(RoundedCornerShape(Radius.small), DinnerDeciderShapes.small)
        assertEquals(RoundedCornerShape(Radius.medium), DinnerDeciderShapes.medium)
        assertEquals(RoundedCornerShape(Radius.card), DinnerDeciderShapes.large)
        assertEquals(RoundedCornerShape(Radius.large), DinnerDeciderShapes.extraLarge)
    }
}
