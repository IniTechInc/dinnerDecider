package com.philwoolley.dinnerdecider.designsystem

import android.database.ContentObserver
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.AnimationSpec
import androidx.compose.animation.core.Easing
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.InfiniteTransition
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.StartOffset
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.ProvidableCompositionLocal
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.composed
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.RoundRect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipPath
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.LocalInspectionMode
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import kotlin.math.PI
import kotlin.math.sin

/**
 * A single, shared visual language for every slow moment in DinnerDecider.
 *
 * Ported from `DinnerDecider/Views/Components/LoadingStates.swift`. Rather than four bespoke
 * spinners, the app leans on one warm motif: a gently simmering pot with rising steam, drawn
 * entirely in a Compose [Canvas] — **no image assets**, exactly as the SwiftUI original draws
 * it in shapes. The emotional goal is calm confidence in the kitchen, so the loading states
 * are unhurried, informative, and never feel broken. That matters more here than on iOS:
 * inference is CPU-only on Android (plan finding 0.1) and a call takes materially longer than
 * the ~20 s iOS measures, so these screens are on display for a while.
 *
 * ### Reduce Motion
 * iOS reads `@Environment(\.accessibilityReduceMotion)`. Android has no direct equivalent;
 * the analogue the platform actually exposes is `Settings.Global.ANIMATOR_DURATION_SCALE`,
 * which the system sets to `0f` when the user turns animations off (Developer options, and
 * on most OEM builds the accessibility "remove animations" toggle). [rememberReduceMotion]
 * wraps that reading, and every animation in this file is gated on it — the same discipline
 * `withMotion(_:_:_:)` enforces on iOS.
 *
 * ### Accessibility
 * The pot and the progress bar are decorative and are hidden from the accessibility tree.
 * The rotating status copy is a **polite live region**, the Android analogue of iOS's
 * `.updatesFrequently` trait: without it TalkBack either never re-announces progress or
 * interrupts the user on every rotation.
 */

// MARK: - Copy

/**
 * Central home for loading copy so the tone stays consistent across screens.
 *
 * Every string is byte-identical to `LoadingCopy` in `LoadingStates.swift`, including the
 * ellipses (three periods, never U+2026) and the "about 20 seconds" figure. `LoadingCopyTest`
 * pins them.
 */
object LoadingCopy {

    /** First model load of the session (the ~20 s "into memory" wait). */
    val modelWarmup = listOf(
        "Waking up the chef... (first scan takes about 20 seconds)",
        "Loading Gemma into memory...",
        "Almost ready...",
    )

    /** Reload after the model was freed under memory pressure. */
    val modelReload = listOf(
        "Warming up again (freed memory to keep things stable)...",
        "Loading Gemma back into memory...",
        "Almost ready...",
    )

    /** Playful rotation while recipes are being generated. */
    val recipeThinking = listOf(
        "Peeking into your pantry...",
        "Tasting a few ideas...",
        "Pairing your ingredients...",
        "Simmering some suggestions...",
        "Plating up options...",
    )

    /** Calm one-liner that reassures the model load is expected, not stuck. */
    const val MODEL_EXPLAINER =
        "This all runs on your device, so it takes a moment the first time."

    /** Calm one-liner for recipe generation. */
    const val RECIPE_EXPLAINER =
        "Cooking up ideas from what you have. Everything stays on your device."

    /** How long each rotating line stays on screen. Matches the iOS 2.0 s timer. */
    const val ROTATION_INTERVAL_MS = 2_000L
}

// MARK: - Reduce Motion

/**
 * True when the user has asked the system to remove animations.
 *
 * Reads `Settings.Global.ANIMATOR_DURATION_SCALE`, which is `0f` when animations are off.
 * This is the closest Android has to iOS's `accessibilityReduceMotion`: Android exposes no
 * dedicated "reduce motion" flag, and the animator scale is what the platform itself honours
 * (`ValueAnimator` multiplies every duration by it), so respecting it is both the accessible
 * and the conventional behaviour.
 *
 * A [ContentObserver] keeps it live, so toggling the setting while a scan is running takes
 * effect without a restart. Returns `false` in previews, where there is no real setting to
 * read and a static pot would be a misleading preview.
 */
@Composable
fun rememberReduceMotion(): Boolean = LocalReduceMotion.current ?: observeSystemReduceMotion()

/**
 * Overrides what [rememberReduceMotion] reports for everything below it.
 *
 * [DinnerDeciderTheme] provides it, which is what makes the setting cheap: the observer is
 * registered **once per theme scope** instead of once per animated element. A screen full of
 * [ShimmeringItemRow]s would otherwise register a `ContentObserver` — a Binder round trip to
 * the settings provider — for every skeleton shape on screen.
 *
 * `null` means "nobody has provided a value, go read the system setting", which is what
 * happens for a composable used outside the theme. Tests may provide `true` to exercise the
 * reduced-motion branch without touching a global device setting.
 */
val LocalReduceMotion: ProvidableCompositionLocal<Boolean?> =
    staticCompositionLocalOf { null }

/** The actual system reading, with a live [ContentObserver]. See [rememberReduceMotion]. */
@Composable
internal fun observeSystemReduceMotion(): Boolean {
    if (LocalInspectionMode.current) return false

    val context = LocalContext.current
    var reduced by remember(context) { mutableStateOf(context.animationsAreDisabled()) }

    DisposableEffect(context) {
        val resolver = context.contentResolver
        val observer = object : ContentObserver(Handler(Looper.getMainLooper())) {
            override fun onChange(selfChange: Boolean) {
                reduced = context.animationsAreDisabled()
            }
        }
        // Reading Settings.Global is safe, but registering an observer can throw on hardened
        // or restricted profiles. A failure here must degrade to "animations on", never crash
        // a loading screen.
        runCatching {
            resolver.registerContentObserver(
                Settings.Global.getUriFor(Settings.Global.ANIMATOR_DURATION_SCALE),
                false,
                observer,
            )
        }
        onDispose { runCatching { resolver.unregisterContentObserver(observer) } }
    }

    return reduced
}

private fun android.content.Context.animationsAreDisabled(): Boolean =
    runCatching {
        Settings.Global.getFloat(contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f)
    }.getOrDefault(1f) == 0f

/**
 * The Android analogue of iOS's `withMotion(_:_:_:)`.
 *
 * iOS passes a `nil` animation when Reduce Motion is on, which makes the change instant.
 * [snap] is the Compose equivalent, so wrapping a spec in this keeps state changes correct
 * (the value still updates) while removing the movement.
 *
 * ```
 * val reduceMotion = rememberReduceMotion()
 * val offset by animateDpAsState(target, tween(300).orSnap(reduceMotion))
 * ```
 */
fun <T> FiniteAnimationSpec<T>.orSnap(reduceMotion: Boolean): AnimationSpec<T> =
    if (reduceMotion) snap() else this

/**
 * [InfiniteTransition.animateFloat], but collapsed to a constant when motion is reduced.
 *
 * This is where the Reduce-Motion contract is actually enforced for this file. Taking the
 * static branch does not merely shorten the animation — it never registers one with the
 * transition at all, so no frame callback is scheduled and the mark is genuinely still rather
 * than animating at zero duration.
 *
 * @param staticValue the resting value under Reduce Motion. It is deliberately a separate
 *   argument rather than `initial` or `target`: iOS settles the glow at a mid-point (scale
 *   1.0, opacity 0.8) that is neither end of its travel.
 * @param delayMillis staggers one wisp against the next, matching SwiftUI's `.delay(...)`.
 */
@Composable
private fun InfiniteTransition.animateFloatOrStatic(
    reduceMotion: Boolean,
    staticValue: Float,
    initial: Float,
    target: Float,
    durationMillis: Int,
    label: String,
    delayMillis: Int = 0,
    easing: Easing = FastOutSlowInEasing,
    repeatMode: RepeatMode = RepeatMode.Reverse,
): State<Float> =
    if (reduceMotion) {
        remember(staticValue) { mutableFloatStateOf(staticValue) }
    } else {
        animateFloat(
            initialValue = initial,
            targetValue = target,
            animationSpec = infiniteRepeatable(
                animation = tween(durationMillis = durationMillis, easing = easing),
                repeatMode = repeatMode,
                initialStartOffset = StartOffset(delayMillis),
            ),
            label = label,
        )
    }

// MARK: - Simmering pot mark

/**
 * The shared hero mark: a warm pot with softly rising steam and a gentle heat glow.
 *
 * Drawn entirely with [Canvas] primitives at any [size] — no drawable, no vector asset, no
 * Lottie. Geometry is a direct transcription of the SwiftUI `ZStack`, with every dimension
 * expressed as a fraction of the mark's side so it scales cleanly.
 *
 * **Purely decorative.** iOS marks it `.accessibilityHidden(true)`; the Compose equivalent is
 * [clearAndSetSemantics] with an empty block, which removes it and its children from the
 * accessibility tree entirely. The status copy beside it carries the meaning.
 */
@Composable
fun SimmerPotMark(
    modifier: Modifier = Modifier,
    size: Dp = 96.dp,
) {
    val reduceMotion = rememberReduceMotion()
    val primary = MaterialTheme.colorScheme.primary
    val accent = MaterialTheme.colorScheme.tertiary

    val transition = rememberInfiniteTransition(label = "simmer")

    // Heat glow: 0.9 -> 1.06 scale and 0.65 -> 0.95 opacity over 2.2 s, mirroring iOS.
    val glowScale: State<Float> = transition.animateFloatOrStatic(
        reduceMotion = reduceMotion,
        staticValue = 1.0f,
        initial = 0.9f,
        target = 1.06f,
        durationMillis = 2_200,
        label = "glow-scale",
    )
    val glowAlpha: State<Float> = transition.animateFloatOrStatic(
        reduceMotion = reduceMotion,
        staticValue = 0.8f,
        initial = 0.65f,
        target = 0.95f,
        durationMillis = 2_200,
        label = "glow-alpha",
    )

    // Three steam wisps, staggered by 0.35 s exactly as the SwiftUI `.delay(Double(i) * 0.35)`.
    val wispPhases: List<State<Float>> = List(STEAM_WISP_COUNT) { i ->
        transition.animateFloatOrStatic(
            reduceMotion = reduceMotion,
            staticValue = 0f,
            initial = 0f,
            target = 1f,
            durationMillis = 2_400,
            delayMillis = i * 350,
            label = "steam-$i",
        )
    }

    Canvas(
        modifier = modifier
            .size(size)
            // Decorative: iOS uses .accessibilityHidden(true).
            .clearAndSetSemantics {},
    ) {
        val d = this.size.minDimension
        drawHeatGlow(d, primary, accent, glowScale.value, glowAlpha.value)
        wispPhases.forEachIndexed { i, phase -> drawSteamWisp(d, i, phase.value, accent) }
        drawPot(d, primary, accent)
    }
}

private const val STEAM_WISP_COUNT = 3

/** Soft radiating warmth behind the pot. */
private fun DrawScope.drawHeatGlow(
    d: Float,
    primary: Color,
    accent: Color,
    scale: Float,
    alpha: Float,
) {
    val radius = d * 0.55f * scale
    drawCircle(
        brush = Brush.radialGradient(
            0.0f to primary.copy(alpha = 0.28f),
            0.5f to accent.copy(alpha = 0.10f),
            1.0f to Color.Transparent,
            center = center,
            radius = radius,
        ),
        radius = radius,
        center = center,
        alpha = alpha,
    )
}

/**
 * One tapering sine wave, rising from the lid.
 *
 * `phase` shifts the wave so the steam shimmers and drifts. The vertical gradient reproduces
 * the SwiftUI `.mask(LinearGradient(colors: [.clear, .black], ...))`: opaque at the lid,
 * fading to nothing as it rises.
 */
private fun DrawScope.drawSteamWisp(d: Float, index: Int, phase: Float, color: Color) {
    val wispWidth = d * 0.13f
    val wispHeight = d * 0.34f
    val spacing = d * 0.06f
    val totalWidth = STEAM_WISP_COUNT * wispWidth + (STEAM_WISP_COUNT - 1) * spacing

    val left = center.x - totalWidth / 2f + index * (wispWidth + spacing)
    val bottom = center.y - d * 0.11f
    val top = bottom - wispHeight
    val rect = Rect(Offset(left, top), Size(wispWidth, wispHeight))

    val steps = 16
    val amplitude = rect.width * 0.45f
    val path = Path()
    for (i in 0..steps) {
        val t = i.toFloat() / steps
        val y = rect.bottom - t * rect.height
        // Taper: narrow at the base, wider as it rises.
        val x = rect.center.x + sin(t * 2.4f * PI.toFloat() + phase * 2f * PI.toFloat()) * amplitude * t
        if (i == 0) path.moveTo(x, y) else path.lineTo(x, y)
    }

    drawPath(
        path = path,
        brush = Brush.verticalGradient(
            colors = listOf(Color.Transparent, color.copy(alpha = 0.55f)),
            startY = rect.top,
            endY = rect.bottom,
        ),
        style = Stroke(width = d * 0.026f, cap = StrokeCap.Round),
    )
}

/** Pot body, lid, knob, and side handles. */
private fun DrawScope.drawPot(d: Float, primary: Color, accent: Color) {
    fun capsule(width: Float, height: Float, dx: Float, dy: Float, color: Color) {
        drawRoundRect(
            color = color,
            topLeft = Offset(center.x + dx - width / 2f, center.y + dy - height / 2f),
            size = Size(width, height),
            cornerRadius = CornerRadius(height / 2f, height / 2f),
        )
    }

    // Handles.
    capsule(d * 0.12f, d * 0.07f, -d * 0.30f, d * 0.14f, primary.copy(alpha = 0.9f))
    capsule(d * 0.12f, d * 0.07f, d * 0.30f, d * 0.14f, primary.copy(alpha = 0.9f))

    // Body.
    val bodyWidth = d * 0.54f
    val bodyHeight = d * 0.30f
    val bodyTop = center.y + d * 0.15f - bodyHeight / 2f
    drawRoundRect(
        brush = Brush.verticalGradient(
            colors = listOf(primary, primary.copy(alpha = 0.78f)),
            startY = bodyTop,
            endY = bodyTop + bodyHeight,
        ),
        topLeft = Offset(center.x - bodyWidth / 2f, bodyTop),
        size = Size(bodyWidth, bodyHeight),
        cornerRadius = CornerRadius(d * 0.10f, d * 0.10f),
    )

    // Lid, then knob.
    capsule(d * 0.60f, d * 0.10f, 0f, -d * 0.02f, primary.copy(alpha = 0.92f))
    capsule(d * 0.13f, d * 0.05f, 0f, -d * 0.08f, accent)
}

// MARK: - Indeterminate progress

/**
 * A subtle indeterminate bar in the brand tint: a soft highlight sweeps across a faint track.
 *
 * Under Reduce Motion it settles into a calm static segment at 30 % rather than disappearing,
 * so the screen still reads as "working" — the same compromise iOS makes. Decorative; the
 * status copy above it is what assistive tech announces.
 */
@Composable
fun SimmerProgressBar(modifier: Modifier = Modifier) {
    val reduceMotion = rememberReduceMotion()
    val primary = MaterialTheme.colorScheme.primary
    val track = MaterialTheme.colorScheme.outlineVariant

    val transition = rememberInfiniteTransition(label = "simmer-progress")
    val sweep: State<Float> = transition.animateFloatOrStatic(
        reduceMotion = reduceMotion,
        staticValue = 0f,
        initial = 0f,
        target = 1f,
        durationMillis = 1_500,
        repeatMode = RepeatMode.Restart,
        label = "sweep",
    )

    Canvas(
        modifier = modifier
            .fillMaxWidth()
            .height(PROGRESS_BAR_HEIGHT)
            .clearAndSetSemantics {},
    ) {
        val w = size.width
        val h = size.height
        val radius = CornerRadius(h / 2f, h / 2f)

        drawRoundRect(color = track.copy(alpha = 0.35f), cornerRadius = radius)

        val highlightWidth = w * 0.4f
        val x = if (reduceMotion) {
            w * 0.3f
        } else {
            -highlightWidth + sweep.value * (w + highlightWidth)
        }

        val trackPath = Path().apply {
            addRoundRect(
                RoundRect(
                    rect = Rect(Offset.Zero, size),
                    cornerRadius = radius,
                )
            )
        }
        clipPath(trackPath) {
            drawRoundRect(
                brush = Brush.horizontalGradient(
                    colors = listOf(primary.copy(alpha = 0.35f), primary),
                    startX = x,
                    endX = x + highlightWidth,
                ),
                topLeft = Offset(x, 0f),
                size = Size(highlightWidth, h),
                cornerRadius = radius,
            )
        }
    }
}

private val PROGRESS_BAR_HEIGHT = 6.dp
private val PROGRESS_BAR_MAX_WIDTH = 220.dp

// MARK: - Full loading state

/**
 * The composed loading treatment used for model warm-up and recipe generation: the simmering
 * pot, a rotating status line, a calm explainer, an indeterminate bar, and an optional Cancel
 * button. One component, so every slow screen shares the same visual language.
 *
 * The status block is a **polite live region** carrying the message and explainer as one
 * label. That is the Android counterpart of iOS's
 * `.accessibilityElement(children: .combine)` + `.accessibilityAddTraits(.updatesFrequently)`.
 * `Polite` and not `Assertive` on purpose: copy rotates every two seconds, and an assertive
 * region would cut the user off mid-sentence, twice a rotation, for the length of a scan.
 *
 * @param messages rotated every [LoadingCopy.ROTATION_INTERVAL_MS]. A single-element list is
 *   shown statically, and an empty list renders no status line rather than crashing.
 * @param onCancel when non-null, a Cancel button appears at the bottom.
 */
@Composable
fun LoadingStateView(
    messages: List<String>,
    modifier: Modifier = Modifier,
    explainer: String? = null,
    showsProgressBar: Boolean = true,
    markSize: Dp = 100.dp,
    cancelTitle: String = "Cancel",
    onCancel: (() -> Unit)? = null,
) {
    val reduceMotion = rememberReduceMotion()
    val haptics = LocalHapticFeedback.current
    var index by remember(messages) { mutableIntStateOf(0) }

    LaunchedEffect(messages) {
        if (messages.size <= 1) return@LaunchedEffect
        while (true) {
            delay(LoadingCopy.ROTATION_INTERVAL_MS)
            index = (index + 1) % messages.size
        }
    }

    val currentMessage = messages.getOrNull(index.coerceAtMost(messages.lastIndex)).orEmpty()
    val announcement = if (explainer != null) "$currentMessage. $explainer" else currentMessage

    Column(
        modifier = modifier.fillMaxSize(),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(Spacing.lg),
    ) {
        Spacer(Modifier.weight(1f))

        SimmerPotMark(size = markSize)

        Column(
            modifier = Modifier
                .padding(horizontal = Spacing.xl)
                .semantics(mergeDescendants = true) {
                    // iOS: .accessibilityAddTraits(.updatesFrequently)
                    liveRegion = LiveRegionMode.Polite
                    contentDescription = announcement
                },
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(Spacing.sm),
        ) {
            Crossfade(
                targetState = currentMessage,
                animationSpec = tween(durationMillis = if (reduceMotion) 0 else 250),
                label = "loading-status",
            ) { message ->
                Text(
                    text = message,
                    style = MaterialTheme.typography.titleMedium,
                    color = MaterialTheme.colorScheme.onSurface,
                    textAlign = TextAlign.Center,
                )
            }
            if (explainer != null) {
                Text(
                    text = explainer,
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center,
                )
            }
        }

        if (showsProgressBar) {
            SimmerProgressBar(
                modifier = Modifier
                    .widthIn(max = PROGRESS_BAR_MAX_WIDTH)
                    .fillMaxWidth()
                    .padding(horizontal = Spacing.xl),
            )
        }

        Spacer(Modifier.weight(1f))

        if (onCancel != null) {
            TextButton(
                onClick = {
                    haptics.performHapticFeedback(HapticFeedbackType.LongPress)
                    onCancel()
                },
                modifier = Modifier.padding(bottom = Spacing.xl),
            ) {
                Text(cancelTitle, style = MaterialTheme.typography.labelLarge)
            }
        }
    }
}

// MARK: - Shimmer

/**
 * A moving highlight that sweeps across the modified element, clipped to its own pixels.
 *
 * Skipped entirely under Reduce Motion, where the element renders as a plain static shape.
 * Apply to greige skeleton placeholders.
 *
 * The sweep is drawn with [BlendMode.SrcAtop] inside an offscreen layer
 * ([CompositingStrategy.Offscreen]); that layer is what makes the blend mask to the content
 * rather than to whatever happens to be painted behind it, and is the Compose equivalent of
 * SwiftUI's `.mask(content)`. Without it the highlight bleeds across the whole bounding box.
 */
fun Modifier.shimmering(): Modifier = composed {
    val reduceMotion = rememberReduceMotion()
    if (reduceMotion) {
        this
    } else {
        val transition = rememberInfiniteTransition(label = "shimmer")
        val progress = transition.animateFloatOrStatic(
            reduceMotion = false,
            staticValue = 0f,
            initial = 0f,
            target = 1f,
            durationMillis = 1_300,
            easing = LinearEasing,
            repeatMode = RepeatMode.Restart,
            label = "shimmer-sweep",
        )
        this
            .graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
            .drawWithContent {
                drawContent()
                val w = size.width
                // Reading progress here keeps the animation in the draw phase: no recomposition
                // per frame, which matters for a list of shimmering rows.
                val x = -w + progress.value * (2f * w)
                drawRect(
                    brush = Brush.horizontalGradient(
                        colors = listOf(
                            Color.Transparent,
                            Color.White.copy(alpha = 0.5f),
                            Color.Transparent,
                        ),
                        startX = x,
                        endX = x + w * 1.2f,
                    ),
                    blendMode = BlendMode.SrcAtop,
                )
            }
    }
}

/** A skeleton bar for placeholder rows. */
@Composable
private fun SkeletonBar(width: Dp, height: Dp, fill: Color) {
    Canvas(
        modifier = Modifier
            .size(width, height)
            .shimmering(),
    ) {
        drawRoundRect(
            color = fill,
            cornerRadius = CornerRadius(size.height / 2f, size.height / 2f),
        )
    }
}

/**
 * A gently shimmering placeholder row shown for the item currently being identified during a
 * scan, so there is always visible motion between results.
 *
 * @param statusLabel what a screen reader announces, e.g. "Looking at item 3 of 8...". The
 *   row's decorative skeleton shapes are cleared from the tree and replaced by this single
 *   label — iOS's `.accessibilityElement(children: .ignore)` — and it is a polite live region
 *   so TalkBack re-reads it as the scan advances without interrupting.
 */
@Composable
fun ShimmeringItemRow(
    statusLabel: String,
    modifier: Modifier = Modifier,
) {
    val fill = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)
    Row(
        modifier = modifier
            .fillMaxWidth()
            .padding(vertical = Spacing.xs)
            .clearAndSetSemantics {
                contentDescription = statusLabel
                liveRegion = LiveRegionMode.Polite
            },
        horizontalArrangement = Arrangement.spacedBy(Spacing.md),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Canvas(
            modifier = Modifier
                .size(28.dp)
                .shimmering(),
        ) {
            drawCircle(color = fill)
        }
        Column(verticalArrangement = Arrangement.spacedBy(Spacing.sm)) {
            SkeletonBar(width = 150.dp, height = 13.dp, fill = fill)
            SkeletonBar(width = 90.dp, height = 10.dp, fill = fill)
        }
        Spacer(Modifier.weight(1f))
    }
}

/**
 * A shimmering placeholder tile shown while a picked photo is being downscaled and added,
 * matching the capture thumbnail footprint. Decorative — the surrounding capture screen
 * announces "Adding photos".
 */
@Composable
fun ShimmerThumbnail(modifier: Modifier = Modifier) {
    val fill = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)
    Canvas(
        modifier = modifier
            .size(width = 120.dp, height = 150.dp)
            .shimmering()
            .clearAndSetSemantics {},
    ) {
        drawRoundRect(
            color = fill,
            cornerRadius = CornerRadius(Radius.card.toPx(), Radius.card.toPx()),
        )
    }
}

// MARK: - Previews

@Preview(name = "Model warm-up", showBackground = true)
@Composable
private fun LoadingStateModelWarmupPreview() {
    DinnerDeciderTheme {
        LoadingStateView(
            messages = LoadingCopy.modelWarmup,
            explainer = LoadingCopy.MODEL_EXPLAINER,
        )
    }
}

@Preview(name = "Recipe thinking (dark)", showBackground = true)
@Composable
private fun LoadingStateRecipeThinkingPreview() {
    DinnerDeciderTheme(darkTheme = true) {
        LoadingStateView(
            messages = LoadingCopy.recipeThinking,
            explainer = LoadingCopy.RECIPE_EXPLAINER,
            onCancel = {},
        )
    }
}

@Preview(name = "Shimmer row", showBackground = true)
@Composable
private fun ShimmeringItemRowPreview() {
    DinnerDeciderTheme {
        Column {
            ShimmeringItemRow(statusLabel = "Looking at item 3 of 8...")
            ShimmerThumbnail()
        }
    }
}
