package com.philwoolley.dinnerdecider.contract

import kotlin.math.roundToInt
import kotlin.math.sqrt

/**
 * The 256-entry 8-bit lookup table for the pre-inference tone curve.
 *
 * iOS applies Core Image's `CIToneCurve` with these five control points before
 * handing a photo to the vision model (`GemmaLLMService.swift:168-174`):
 *
 * ```
 * (0.00, 0.05)  (0.25, 0.28)  (0.50, 0.55)  (0.75, 0.78)  (1.00, 1.00)
 * ```
 *
 * It lifts the shadows (black maps to 0.05) and adds a little mid-tone contrast,
 * which is what makes label text in a dim fridge legible to the model. Android
 * has no `CIToneCurve`, so the curve is reproduced here as a plain [IntArray]
 * built by **monotone cubic interpolation** (Fritsch-Carlson) through the same
 * five points: monotone by construction, so the curve can never invert between
 * control points the way a plain Catmull-Rom spline can.
 *
 * Being a pure function of nothing, the curve is contract-checked on the host
 * JVM with no device: zero `android.*` imports. Plan Step 28 applies it.
 *
 * **Order matters where it is applied:** the tone curve runs at **full
 * resolution, before** the downscale to 896 px, mirroring iOS. Moving it after
 * the downscale silently changes what the model sees - a looks-like-success
 * recognition regression, not a compile error.
 */
object ToneCurveLut {

    /** The control points, verbatim from the iOS `CIToneCurve` parameters. */
    val CONTROL_POINTS: List<Pair<Double, Double>> = listOf(
        0.0 to 0.05,
        0.25 to 0.28,
        0.5 to 0.55,
        0.75 to 0.78,
        1.0 to 1.0,
    )

    private val XS: DoubleArray = CONTROL_POINTS.map { it.first }.toDoubleArray()
    private val YS: DoubleArray = CONTROL_POINTS.map { it.second }.toDoubleArray()

    /**
     * Fritsch-Carlson tangents: start from the average of the neighbouring
     * secants, then rescale any tangent pair that would overshoot. The rescale is
     * what guarantees monotonicity.
     *
     * Declared **before** [table] on purpose: an `object`'s property initializers
     * run in declaration order, and [table] calls [evaluate], which reads this.
     */
    private val TANGENTS: DoubleArray = computeTangents()

    /**
     * `table[i]` is the output byte for input byte `i`, both 0..255.
     *
     * Monotonically non-decreasing across all 256 entries, `table[0] == 13`
     * (0.05 * 255 rounded) and `table[255] == 255`.
     */
    val table: IntArray = IntArray(256) { index ->
        (evaluate(index / 255.0) * 255.0).roundToInt().coerceIn(0, 255)
    }

    /** Map one 8-bit channel value through the curve. Out-of-range input clamps. */
    fun apply(channel: Int): Int = table[channel.coerceIn(0, 255)]

    /**
     * Evaluate the continuous curve at [x] in 0..1. Exposed so a test can check
     * the control points without going through the 8-bit quantisation.
     */
    fun evaluate(x: Double): Double {
        if (x <= XS.first()) return YS.first()
        if (x >= XS.last()) return YS.last()

        var i = 0
        while (i < XS.size - 2 && x > XS[i + 1]) i++

        val h = XS[i + 1] - XS[i]
        val t = (x - XS[i]) / h
        val t2 = t * t
        val t3 = t2 * t
        // Cubic Hermite basis functions.
        val h00 = 2 * t3 - 3 * t2 + 1
        val h10 = t3 - 2 * t2 + t
        val h01 = -2 * t3 + 3 * t2
        val h11 = t3 - t2
        return YS[i] * h00 + h * TANGENTS[i] * h10 + YS[i + 1] * h01 + h * TANGENTS[i + 1] * h11
    }

    private fun computeTangents(): DoubleArray {
        val n = XS.size
        val secants = DoubleArray(n - 1) { (YS[it + 1] - YS[it]) / (XS[it + 1] - XS[it]) }

        val m = DoubleArray(n)
        m[0] = secants[0]
        m[n - 1] = secants[n - 2]
        for (i in 1 until n - 1) {
            m[i] = (secants[i - 1] + secants[i]) / 2.0
        }

        for (i in 0 until n - 1) {
            val d = secants[i]
            if (d == 0.0) {
                // A flat segment must stay flat, or the spline would dip.
                m[i] = 0.0
                m[i + 1] = 0.0
            } else {
                val alpha = m[i] / d
                val beta = m[i + 1] / d
                val squared = alpha * alpha + beta * beta
                if (squared > 9.0) {
                    val tau = 3.0 / sqrt(squared)
                    m[i] = tau * alpha * d
                    m[i + 1] = tau * beta * d
                }
            }
        }
        return m
    }
}
