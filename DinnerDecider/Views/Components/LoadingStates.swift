import SwiftUI

/// A single, shared visual language for every slow moment in DinnerDecider.
///
/// Rather than four bespoke spinners, the app leans on one warm motif: a gently
/// simmering pot with rising steam, drawn entirely in SwiftUI shapes (no assets).
/// The emotional goal is calm confidence in the kitchen, so the loading states
/// are unhurried, informative, and never feel broken.
///
/// Everything here respects Reduce Motion via the same rule the rest of the app
/// uses: when motion is reduced, animations fall back to a static icon plus text.
/// Loading views announce their status to VoiceOver and carry the
/// `.updatesFrequently` trait so assistive tech re-reads them as copy rotates.

// MARK: - Copy

/// Central home for loading copy so the tone stays consistent across screens.
enum LoadingCopy {
    /// First model load of the session (the ~20s "into memory" wait).
    static let modelWarmup = [
        "Waking up the chef... (first scan takes about 20 seconds)",
        "Loading Gemma into memory...",
        "Almost ready..."
    ]

    /// Reload after the model was freed under memory pressure.
    static let modelReload = [
        "Warming up again (freed memory to keep things stable)...",
        "Loading Gemma back into memory...",
        "Almost ready..."
    ]

    /// Playful rotation while recipes are being generated.
    static let recipeThinking = [
        "Peeking into your pantry...",
        "Tasting a few ideas...",
        "Pairing your ingredients...",
        "Simmering some suggestions...",
        "Plating up options..."
    ]

    /// Calm one-liner that reassures the model load is expected, not stuck.
    static let modelExplainer = "This all runs on your device, so it takes a moment the first time."

    /// Calm one-liner for recipe generation.
    static let recipeExplainer = "Cooking up ideas from what you have. Everything stays on your device."
}

// MARK: - Simmering pot mark

/// The shared hero mark: a warm pot with softly rising steam and a gentle heat
/// glow. Scales to `size`. Purely decorative, hidden from VoiceOver.
struct SimmerPotMark: View {
    var size: CGFloat = 96

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var animate = false

    var body: some View {
        ZStack {
            heatGlow
            steam
            pot
        }
        .frame(width: size, height: size)
        .onAppear { animate = true }
        .accessibilityHidden(true)
    }

    // Soft radiating warmth behind the pot.
    private var heatGlow: some View {
        Circle()
            .fill(
                RadialGradient(
                    colors: [Color.brandPrimary.opacity(0.28), Color.brandAccent.opacity(0.10), .clear],
                    center: .center,
                    startRadius: 0,
                    endRadius: size * 0.55
                )
            )
            .scaleEffect(reduceMotion ? 1.0 : (animate ? 1.06 : 0.9))
            .opacity(reduceMotion ? 0.8 : (animate ? 0.95 : 0.65))
            .animation(
                reduceMotion ? nil : .easeInOut(duration: 2.2).repeatForever(autoreverses: true),
                value: animate
            )
    }

    // Three staggered steam wisps rising from the lid.
    private var steam: some View {
        HStack(spacing: size * 0.06) {
            ForEach(0..<3, id: \.self) { i in
                SteamWisp(phase: reduceMotion ? 0 : (animate ? 1 : 0))
                    .stroke(style: StrokeStyle(lineWidth: size * 0.026, lineCap: .round))
                    .foregroundStyle(Color.brandAccent.opacity(0.55))
                    .frame(width: size * 0.13, height: size * 0.34)
                    .mask(
                        LinearGradient(colors: [.clear, .black], startPoint: .top, endPoint: .bottom)
                    )
                    .animation(
                        reduceMotion ? nil : .easeInOut(duration: 2.4)
                            .repeatForever(autoreverses: true)
                            .delay(Double(i) * 0.35),
                        value: animate
                    )
            }
        }
        .offset(y: -size * 0.28)
    }

    // Pot body, lid, knob, and side handles.
    private var pot: some View {
        ZStack {
            // Handles
            Capsule()
                .fill(Color.brandPrimary.opacity(0.9))
                .frame(width: size * 0.12, height: size * 0.07)
                .offset(x: -size * 0.30, y: size * 0.14)
            Capsule()
                .fill(Color.brandPrimary.opacity(0.9))
                .frame(width: size * 0.12, height: size * 0.07)
                .offset(x: size * 0.30, y: size * 0.14)

            // Body
            RoundedRectangle(cornerRadius: size * 0.10, style: .continuous)
                .fill(
                    LinearGradient(
                        colors: [Color.brandPrimary, Color.brandPrimary.opacity(0.78)],
                        startPoint: .top,
                        endPoint: .bottom
                    )
                )
                .frame(width: size * 0.54, height: size * 0.30)
                .offset(y: size * 0.15)

            // Lid
            Capsule()
                .fill(Color.brandPrimary.opacity(0.92))
                .frame(width: size * 0.60, height: size * 0.10)
                .offset(y: -size * 0.02)

            // Knob
            Capsule()
                .fill(Color.brandAccent)
                .frame(width: size * 0.13, height: size * 0.05)
                .offset(y: -size * 0.08)
        }
    }
}

/// A tapering sine wave used for a single steam wisp. `phase` shifts the wave so
/// the steam appears to shimmer and drift as it rises.
private struct SteamWisp: Shape {
    var phase: CGFloat

    var animatableData: CGFloat {
        get { phase }
        set { phase = newValue }
    }

    func path(in rect: CGRect) -> Path {
        var path = Path()
        let steps = 16
        let amplitude = rect.width * 0.45
        for i in 0...steps {
            let t = CGFloat(i) / CGFloat(steps)
            let y = rect.maxY - t * rect.height
            // Taper the wave so it is narrow at the base and wider as it rises.
            let x = rect.midX + sin((t * 2.4 * .pi) + phase * 2 * .pi) * amplitude * t
            if i == 0 {
                path.move(to: CGPoint(x: x, y: y))
            } else {
                path.addLine(to: CGPoint(x: x, y: y))
            }
        }
        return path
    }
}

// MARK: - Indeterminate progress

/// A subtle indeterminate bar in the brand tint. A soft highlight sweeps across a
/// faint track. Under Reduce Motion it settles into a calm static segment.
struct SimmerProgressBar: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var animating = false

    var body: some View {
        GeometryReader { geo in
            let width = geo.size.width
            Capsule()
                .fill(Color.separatorNeutral.opacity(0.35))
                .overlay(alignment: .leading) {
                    Capsule()
                        .fill(
                            LinearGradient(
                                colors: [Color.brandPrimary.opacity(0.35), Color.brandPrimary],
                                startPoint: .leading,
                                endPoint: .trailing
                            )
                        )
                        .frame(width: width * 0.4)
                        .offset(x: reduceMotion ? width * 0.3 : (animating ? width : -width * 0.4))
                }
                .clipShape(Capsule())
        }
        .frame(height: 6)
        .onAppear {
            guard !reduceMotion else { return }
            withAnimation(.easeInOut(duration: 1.5).repeatForever(autoreverses: false)) {
                animating = true
            }
        }
        .accessibilityHidden(true)
    }
}

// MARK: - Full loading state

/// The composed loading treatment used for model warm-up and recipe generation:
/// the simmering pot, a rotating status line, a calm explainer, an indeterminate
/// bar, and an optional Cancel button. One component so every slow screen shares
/// the same visual language.
struct LoadingStateView: View {
    var messages: [String]
    var explainer: String?
    var showsProgressBar: Bool = true
    var markSize: CGFloat = 100
    var onCancel: (() -> Void)?
    var cancelTitle: String = "Cancel"

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var index = 0
    private let timer = Timer.publish(every: 2.0, on: .main, in: .common).autoconnect()

    private var currentMessage: String {
        guard !messages.isEmpty else { return "" }
        return messages[min(index, messages.count - 1)]
    }

    var body: some View {
        VStack(spacing: Spacing.lg) {
            Spacer(minLength: 0)

            SimmerPotMark(size: markSize)

            VStack(spacing: Spacing.sm) {
                Text(currentMessage)
                    .font(.dmHeadline)
                    .foregroundStyle(Color.textPrimary)
                    .multilineTextAlignment(.center)
                    .contentTransition(.opacity)
                    .id(index)
                if let explainer {
                    Text(explainer)
                        .font(.dmFootnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
            }
            .padding(.horizontal, Spacing.xl)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(explainer.map { "\(currentMessage). \($0)" } ?? currentMessage)
            .accessibilityAddTraits(.updatesFrequently)

            if showsProgressBar {
                SimmerProgressBar()
                    .frame(maxWidth: 220)
                    .padding(.horizontal, Spacing.xl)
            }

            Spacer(minLength: 0)

            if let onCancel {
                Button(cancelTitle, role: .cancel) {
                    Haptics.tap()
                    onCancel()
                }
                .padding(.bottom, Spacing.xl)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .onReceive(timer) { _ in advance() }
    }

    private func advance() {
        guard messages.count > 1 else { return }
        let next = (index + 1) % messages.count
        withMotion(reduceMotion, .easeInOut) { index = next }
    }
}

// MARK: - Shimmer

/// A moving highlight that sweeps across the modified view, masked to its shape.
/// Skipped entirely under Reduce Motion. Apply to greige skeleton shapes.
private struct ShimmerEffect: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var animating = false

    func body(content: Content) -> some View {
        content.overlay {
            if !reduceMotion {
                GeometryReader { geo in
                    let width = geo.size.width
                    LinearGradient(
                        colors: [.clear, Color.white.opacity(0.5), .clear],
                        startPoint: .leading,
                        endPoint: .trailing
                    )
                    .frame(width: width * 1.2)
                    .offset(x: animating ? width : -width)
                }
                .mask(content)
                .allowsHitTesting(false)
                .onAppear {
                    withAnimation(.linear(duration: 1.3).repeatForever(autoreverses: false)) {
                        animating = true
                    }
                }
            }
        }
    }
}

extension View {
    /// Adds a shimmering sweep (used on skeleton placeholders). Respects Reduce
    /// Motion, where it renders as a plain static shape.
    func shimmering() -> some View {
        modifier(ShimmerEffect())
    }
}

/// A skeleton bar for placeholder rows.
private struct SkeletonBar: View {
    var width: CGFloat
    var height: CGFloat = 12

    var body: some View {
        RoundedRectangle(cornerRadius: height / 2, style: .continuous)
            .fill(Color.separatorNeutral.opacity(0.5))
            .frame(width: width, height: height)
            .shimmering()
    }
}

/// A gently shimmering placeholder row shown for the item currently being
/// identified during a scan, so there is always visible motion between results.
/// Its `statusLabel` ("Looking at item 3 of 8...") is what VoiceOver announces.
struct ShimmeringItemRow: View {
    var statusLabel: String

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(Color.separatorNeutral.opacity(0.5))
                .frame(width: 28, height: 28)
                .shimmering()
            VStack(alignment: .leading, spacing: 8) {
                SkeletonBar(width: 150, height: 13)
                SkeletonBar(width: 90, height: 10)
            }
            Spacer()
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(statusLabel)
        .accessibilityAddTraits(.updatesFrequently)
    }
}

/// A shimmering placeholder tile shown while a picked photo is being downscaled
/// and added, matching the capture thumbnail footprint.
struct ShimmerThumbnail: View {
    var body: some View {
        RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
            .fill(Color.separatorNeutral.opacity(0.5))
            .frame(width: 120, height: 150)
            .shimmering()
            .accessibilityHidden(true)
    }
}

// MARK: - Previews

#Preview("Model warm-up") {
    LoadingStateView(
        messages: LoadingCopy.modelWarmup,
        explainer: LoadingCopy.modelExplainer
    )
    .background(Color.surfacePrimary)
}

#Preview("Recipe thinking") {
    LoadingStateView(
        messages: LoadingCopy.recipeThinking,
        explainer: LoadingCopy.recipeExplainer,
        onCancel: {}
    )
    .background(Color.surfacePrimary)
}

#Preview("Shimmer row") {
    List {
        ShimmeringItemRow(statusLabel: "Looking at item 3 of 8...")
    }
}
