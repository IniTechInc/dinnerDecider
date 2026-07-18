import SwiftUI

/// Three short first-run screens: photograph, confirm, cook. The privacy line
/// is the hook, so it leads. Shown once, then never again.
struct OnboardingView: View {
    var onDone: () -> Void

    @State private var page = 0

    private struct Slide: Identifiable {
        let id = Int.random(in: Int.min...Int.max)
        let symbol: String
        let title: String
        let body: String
        let tint: Color
    }

    private let slides: [Slide] = [
        Slide(
            symbol: "camera.viewfinder",
            title: "Photograph your food",
            body: "Snap your fridge, pantry, or shelves. DinnerDecider looks at what you have, one item at a time.",
            tint: .brandPrimary
        ),
        Slide(
            symbol: "checkmark.circle",
            title: "Confirm the list",
            body: "Review what was found and fix anything with a tap. You are always in control of your inventory.",
            tint: .brandSecondary
        ),
        Slide(
            symbol: "fork.knife",
            title: "Cook what fits",
            body: "Get recipes from what you already have, plus a few you are only a couple of items away from making.",
            tint: .brandAccent
        )
    ]

    var body: some View {
        VStack(spacing: 0) {
            TabView(selection: $page) {
                ForEach(Array(slides.enumerated()), id: \.offset) { index, slide in
                    slideView(slide)
                        .tag(index)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .always))
            .indexViewStyle(.page(backgroundDisplayMode: .always))

            privacyNote

            controls
        }
        .background(Color.surfacePrimary.ignoresSafeArea())
        .tint(.brandPrimary)
    }

    private func slideView(_ slide: Slide) -> some View {
        VStack(spacing: 24) {
            Spacer()
            Image(systemName: slide.symbol)
                .font(.system(size: 88, weight: .regular))
                .foregroundStyle(slide.tint)
                .accessibilityHidden(true)
            VStack(spacing: 12) {
                Text(slide.title)
                    .font(.displayLarge)
                    .multilineTextAlignment(.center)
                Text(slide.body)
                    .font(.dmBody)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
            }
            Spacer()
        }
        .padding()
    }

    private var privacyNote: some View {
        HStack(spacing: 8) {
            Image(systemName: "lock.shield.fill")
                .foregroundStyle(Color.brandSecondary)
            Text("Everything stays on your phone. No account, no cloud, works in airplane mode.")
                .font(.dmFootnote)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 12)
        .multilineTextAlignment(.leading)
        .accessibilityElement(children: .combine)
    }

    private var controls: some View {
        VStack(spacing: 12) {
            Button {
                Haptics.tap()
                if page < slides.count - 1 {
                    withAnimation { page += 1 }
                } else {
                    onDone()
                }
            } label: {
                Text(page < slides.count - 1 ? "Next" : "Get started")
                    .font(.dmBodyBold)
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)

            if page < slides.count - 1 {
                Button("Skip") { onDone() }
                    .font(.dmSubheadline)
            }
        }
        .padding(.horizontal, 24)
        .padding(.bottom, 24)
    }
}

#Preview {
    OnboardingView(onDone: {})
}
