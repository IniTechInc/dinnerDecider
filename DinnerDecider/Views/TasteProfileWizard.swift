import SwiftUI

/// A paged wizard that collects the user's taste preferences. Shown as part of
/// first-run onboarding and re-accessible from Settings and Recipes.
///
/// Each question is a wrapping flow of tappable pills plus a small "Other"
/// free-text box, so answering is fast but nothing is locked to the presets.
/// A "Skip for now" button in the top-right means the wizard is never a trap.
struct TasteProfileWizard: View {
    var onComplete: () -> Void

    @State private var profile = TasteProfile.load() ?? TasteProfile()
    @State private var page = 0
    /// Toggled briefly when the spice level changes to give the pepper a wiggle.
    @State private var wiggle = false
    /// Drives the forever-repeating flame flicker at spice level 5.
    @State private var flicker = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private let totalPages = 8 // 0 = intro, 1-6 = questions, 7 = spice

    var body: some View {
        VStack(spacing: 0) {
            // Skip is always available, on every page including the intro.
            HStack {
                Spacer()
                Button("Skip for now") {
                    complete(success: false)
                }
                .font(.dmSubheadline)
                .foregroundStyle(.secondary)
            }
            .padding(.horizontal, Spacing.xl)
            .padding(.top, Spacing.sm)

            // Progress
            if page > 0 {
                ProgressView(value: Double(page), total: Double(totalPages - 1))
                    .tint(.brandPrimary)
                    .padding(.horizontal, Spacing.xl)
                    .padding(.top, Spacing.xs)
            }

            TabView(selection: $page) {
                introPage.tag(0)
                pillPage(
                    tag: 1,
                    title: "What foods do you love?",
                    subtitle: "Tap anything that sounds good to you.",
                    options: Self.favoriteFoodOptions,
                    selection: $profile.favoriteFoods,
                    other: $profile.favoriteFoodsOther
                )
                pillPage(
                    tag: 2,
                    title: "Any foods you'd rather skip?",
                    subtitle: "We'll steer clear of these.",
                    options: Self.dislikedFoodOptions,
                    selection: $profile.dislikedFoods,
                    other: $profile.dislikedFoodsOther
                )
                pillPage(
                    tag: 3,
                    title: "What styles of food do you go out for?",
                    subtitle: "This helps us understand your vibe.",
                    options: Self.cuisinesLovedOptions,
                    selection: $profile.cuisinesLoved,
                    other: $profile.cuisinesLovedOther
                )
                pillPage(
                    tag: 4,
                    title: "Any styles you avoid eating out?",
                    subtitle: "Places or food styles you steer clear of.",
                    options: Self.cuisinesAvoidedOptions,
                    selection: $profile.cuisinesAvoided,
                    other: $profile.cuisinesAvoidedOther
                )
                pillPage(
                    tag: 5,
                    title: "Do you have any food allergies?",
                    subtitle: "Recipes will never include these.",
                    options: Self.allergyOptions,
                    selection: $profile.allergies,
                    other: $profile.allergiesOther
                )
                pillPage(
                    tag: 6,
                    title: "Textures or flavors you avoid?",
                    subtitle: "Whatever puts you off a dish.",
                    options: Self.textureOptions,
                    selection: $profile.texturesAvoided,
                    other: $profile.texturesAvoidedOther
                )
                spicePage.tag(7)
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
            .animation(reduceMotion ? nil : .easeInOut(duration: 0.3), value: page)

            // Navigation buttons
            HStack {
                if page > 0 {
                    Button("Back") {
                        Haptics.tap()
                        page -= 1
                    }
                    .font(.dmBody)
                    .foregroundStyle(.secondary)
                }
                Spacer()
                if page < totalPages - 1 {
                    Button(page == 0 ? "Let's go" : "Next") {
                        Haptics.tap()
                        page += 1
                    }
                    .font(.dmBodyBold)
                    .foregroundStyle(Color.brandPrimary)
                } else {
                    Button("Done") {
                        complete(success: true)
                    }
                    .font(.dmBodyBold)
                    .buttonStyle(.borderedProminent)
                    .tint(.brandPrimary)
                }
            }
            .padding(.horizontal, Spacing.xl)
            .padding(.bottom, Spacing.xl)
        }
        .background(Color.surfacePrimary.ignoresSafeArea())
        .interactiveDismissDisabled()
    }

    /// Persist whatever was answered (even partial) and hand back to the caller.
    /// Skip and Done share this so partial answers are never lost.
    private func complete(success: Bool) {
        profile.save()
        if success {
            Haptics.success()
        } else {
            Haptics.tap()
        }
        onComplete()
    }

    // MARK: - Intro

    private var introPage: some View {
        VStack(spacing: Spacing.xl) {
            Spacer()
            Image(systemName: "fork.knife.circle.fill")
                .font(.system(size: 72))
                .foregroundStyle(Color.brandPrimary)
            Text("Your Taste Profile")
                .font(.displayTitle)
                .foregroundStyle(Color.textPrimary)
            Text("Answer a few quick questions so we can suggest recipes you'll actually love.")
                .font(.dmBody)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, Spacing.xl)
            Spacer()
            Spacer()
        }
        .padding()
    }

    // MARK: - Pill question page

    private func pillPage(
        tag: Int,
        title: String,
        subtitle: String,
        options: [String],
        selection: Binding<[String]>,
        other: Binding<String>
    ) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Spacing.lg) {
                Text(title)
                    .font(.displayTitle)
                    .foregroundStyle(Color.textPrimary)
                Text(subtitle)
                    .font(.dmBody)
                    .foregroundStyle(.secondary)

                FlowLayout(spacing: Spacing.sm) {
                    ForEach(options, id: \.self) { option in
                        pill(option, isSelected: selection.wrappedValue.contains(option)) {
                            toggle(option, in: selection)
                        }
                    }
                }

                otherBox(text: other)
            }
            .padding(.horizontal, Spacing.xl)
            .padding(.top, Spacing.lg)
            .padding(.bottom, Spacing.xl)
        }
        .tag(tag)
    }

    private func pill(_ label: String, isSelected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if isSelected {
                    Image(systemName: "checkmark")
                        .font(.dmCaption.weight(.bold))
                        .transition(reduceMotion ? .identity : .scale.combined(with: .opacity))
                }
                Text(label)
                    .font(.dmBody)
            }
            .padding(.horizontal, Spacing.md)
            .frame(minHeight: 44)
            .foregroundStyle(isSelected ? .white : Color.textPrimary)
            .background(
                Capsule().fill(isSelected ? Color.brandPrimary : Color.surfaceSecondary)
            )
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    private func otherBox(text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            Text("Other")
                .font(.dmCaption)
                .foregroundStyle(.secondary)
            TextField("Anything else?", text: text)
                .font(.dmBody)
                .autocorrectionDisabled()
                .padding(Spacing.md)
                .background(
                    RoundedRectangle(cornerRadius: Radius.medium, style: .continuous)
                        .fill(Color.surfaceSecondary)
                )
        }
    }

    private func toggle(_ option: String, in selection: Binding<[String]>) {
        Haptics.select()
        withMotion(reduceMotion, .spring(response: 0.3, dampingFraction: 0.7)) {
            if let index = selection.wrappedValue.firstIndex(of: option) {
                selection.wrappedValue.remove(at: index)
            } else {
                selection.wrappedValue.append(option)
            }
        }
    }

    // MARK: - Spice page

    private var spicePage: some View {
        VStack(spacing: Spacing.xl) {
            Spacer()
            Text("How spicy do you like your food?")
                .font(.displayTitle)
                .foregroundStyle(Color.textPrimary)
                .multilineTextAlignment(.center)

            chiliPepper
                .frame(height: 130)

            HStack(spacing: Spacing.sm) {
                ForEach(1...5, id: \.self) { level in
                    Button {
                        Haptics.select()
                        profile.spiceLevel = level
                        triggerWiggle()
                    } label: {
                        Text("\(level)")
                            .font(.dmHeadline)
                            .frame(width: 48, height: 48)
                            .background(
                                Circle()
                                    .fill(profile.spiceLevel == level ? Color.brandPrimary : Color.surfaceSecondary)
                            )
                            .foregroundStyle(profile.spiceLevel == level ? .white : Color.textPrimary)
                    }
                }
            }

            HStack {
                Text("Mild")
                    .font(.dmCaption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text("Bring it on")
                    .font(.dmCaption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 40)

            Spacer()
            Spacer()
        }
        .padding(.horizontal, Spacing.xl)
    }

    /// The hero chili: a green pod at level 1 that heats to a deep red pepper on
    /// fire at level 5.
    private var chiliPepper: some View {
        ZStack {
            // Flames only at the hottest level.
            if profile.spiceLevel == 5 {
                flames
                    .offset(y: -58)
                    .transition(reduceMotion ? .identity : .scale.combined(with: .opacity))
            }

            VStack(spacing: -6) {
                ChiliStem()
                    .fill(Color.brandSecondary)
                    .frame(width: 30, height: 34)
                    .rotationEffect(.degrees(-10))
                    .zIndex(1)
                ChiliPod()
                    .fill(podColor)
                    .frame(width: 60, height: 100)
                    .animation(reduceMotion ? nil : .easeInOut(duration: 0.45), value: profile.spiceLevel)
            }
        }
        .scaleEffect(wiggle ? 1.08 : 1.0)
        .rotationEffect(.degrees(wiggle ? -3 : 0))
        .accessibilityElement()
        .accessibilityLabel("Spice level \(profile.spiceLevel) of 5")
    }

    private var flames: some View {
        HStack(alignment: .bottom, spacing: 2) {
            flame(size: 26, phase: 0.0, color: .orange)
            flame(size: 40, phase: 0.2, color: .red)
            flame(size: 26, phase: 0.4, color: .orange)
        }
        .onAppear { flicker = true }
        .accessibilityHidden(true)
    }

    private func flame(size: CGFloat, phase: Double, color: Color) -> some View {
        Image(systemName: "flame.fill")
            .font(.system(size: size))
            .foregroundStyle(color)
            .opacity(flicker ? 1.0 : 0.55)
            .scaleEffect(flicker ? 1.06 : 0.9, anchor: .bottom)
            .animation(
                reduceMotion ? nil : .easeInOut(duration: 0.6).repeatForever(autoreverses: true).delay(phase),
                value: flicker
            )
    }

    /// Pod fill warms from fresh green up to deep hot red.
    private var podColor: Color {
        switch profile.spiceLevel {
        case 1: return Color(red: 0.42, green: 0.72, blue: 0.35) // fresh green
        case 2: return Color(red: 0.63, green: 0.74, blue: 0.24) // yellow-green
        case 3: return Color(red: 0.93, green: 0.68, blue: 0.20) // amber-orange
        case 4: return Color(red: 0.90, green: 0.35, blue: 0.15) // orange-red
        case 5: return Color(red: 0.80, green: 0.12, blue: 0.10) // deep hot red
        default: return Color(red: 0.93, green: 0.68, blue: 0.20)
        }
    }

    private func triggerWiggle() {
        guard !reduceMotion else { return }
        withAnimation(.spring(response: 0.22, dampingFraction: 0.4)) { wiggle = true }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.22) {
            withAnimation(.spring(response: 0.32, dampingFraction: 0.6)) { wiggle = false }
        }
    }

    // MARK: - Pill option lists

    private static let favoriteFoodOptions = [
        "Pizza", "Tacos", "Sushi", "Pasta", "Burgers", "BBQ", "Fried chicken",
        "Ramen", "Curry", "Steak", "Salads", "Sandwiches", "Stir-fry",
        "Seafood", "Breakfast food", "Soup", "Mac and cheese", "Dumplings"
    ]
    private static let dislikedFoodOptions = [
        "Liver", "Anchovies", "Beets", "Olives", "Mushrooms", "Blue cheese",
        "Cilantro", "Tofu", "Brussels sprouts", "Okra", "Raw fish",
        "Shellfish", "Eggplant", "Coconut"
    ]
    private static let cuisinesLovedOptions = [
        "Mexican", "Italian", "Chinese", "Thai", "Japanese", "Indian",
        "Mediterranean", "American diner", "BBQ joint", "Pizza place",
        "Steakhouse", "Vietnamese"
    ]
    private static let cuisinesAvoidedOptions = [
        "Fast food", "Buffets", "Seafood spots", "Vegan spots", "Fine dining",
        "Chain restaurants", "Food trucks", "Late-night diners"
    ]
    private static let allergyOptions = [
        "Peanuts", "Tree nuts", "Dairy", "Eggs", "Gluten", "Soy", "Fish",
        "Shellfish", "Sesame"
    ]
    private static let textureOptions = [
        "Mushy", "Slimy", "Chewy", "Gritty", "Overly sweet", "Very salty",
        "Bitter", "Sour", "Greasy", "Dry"
    ]
}

// MARK: - Chili shapes

/// The pepper pod: a plump, gently curved cone tapering to a pointed tip.
private struct ChiliPod: Shape {
    func path(in rect: CGRect) -> Path {
        let w = rect.width
        let h = rect.height
        var p = Path()
        let topLeft = CGPoint(x: w * 0.30, y: h * 0.08)
        let topRight = CGPoint(x: w * 0.70, y: h * 0.08)
        let tip = CGPoint(x: w * 0.60, y: h * 0.98)
        p.move(to: topLeft)
        // Outer (left) edge bulges out then sweeps down to the tip.
        p.addCurve(
            to: tip,
            control1: CGPoint(x: w * 0.02, y: h * 0.48),
            control2: CGPoint(x: w * 0.34, y: h * 0.94)
        )
        // Inner (right) edge curves back up to the shoulder.
        p.addCurve(
            to: topRight,
            control1: CGPoint(x: w * 0.96, y: h * 0.66),
            control2: CGPoint(x: w * 0.80, y: h * 0.20)
        )
        // Round the shoulder across the top where the stem sits.
        p.addQuadCurve(to: topLeft, control: CGPoint(x: w * 0.50, y: h * -0.04))
        p.closeSubpath()
        return p
    }
}

/// A short curved stem cap that sits on the pod's shoulder.
private struct ChiliStem: Shape {
    func path(in rect: CGRect) -> Path {
        let w = rect.width
        let h = rect.height
        var p = Path()
        p.move(to: CGPoint(x: w * 0.40, y: h * 0.95))
        p.addQuadCurve(
            to: CGPoint(x: w * 0.55, y: h * 0.05),
            control: CGPoint(x: w * 0.34, y: h * 0.40)
        )
        p.addQuadCurve(
            to: CGPoint(x: w * 0.80, y: h * 0.30),
            control: CGPoint(x: w * 0.82, y: h * 0.04)
        )
        p.addQuadCurve(
            to: CGPoint(x: w * 0.62, y: h * 0.98),
            control: CGPoint(x: w * 0.66, y: h * 0.55)
        )
        p.closeSubpath()
        return p
    }
}
