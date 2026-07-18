import SwiftUI

/// A paged wizard that collects the user's taste preferences. Shown on first
/// launch and re-accessible from Settings.
struct TasteProfileWizard: View {
    var onComplete: () -> Void

    @State private var profile = TasteProfile.load() ?? TasteProfile()
    @State private var page = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private let totalPages = 8 // 0 = intro, 1-7 = questions

    var body: some View {
        VStack(spacing: 0) {
            // Progress
            if page > 0 {
                ProgressView(value: Double(page), total: Double(totalPages - 1))
                    .tint(.brandPrimary)
                    .padding(.horizontal, Spacing.xl)
                    .padding(.top, Spacing.sm)
            }

            TabView(selection: $page) {
                introPage.tag(0)
                questionPage(
                    tag: 1,
                    title: "What are your favorite foods?",
                    subtitle: "Think comfort meals, go-to snacks, anything you love.",
                    placeholder: "e.g. tacos, sushi, mac and cheese...",
                    text: $profile.favoriteFoods
                )
                questionPage(
                    tag: 2,
                    title: "What are your least favorite foods?",
                    subtitle: "We'll steer clear of these.",
                    placeholder: "e.g. liver, anchovies, beets...",
                    text: $profile.leastFavoriteFoods
                )
                questionPage(
                    tag: 3,
                    title: "Where do you usually go out to eat?",
                    subtitle: "Restaurant names or styles — helps us understand your vibe.",
                    placeholder: "e.g. Chipotle, local Thai place, BBQ joints...",
                    text: $profile.favoriteRestaurants
                )
                questionPage(
                    tag: 4,
                    title: "Where do you never go out to eat?",
                    subtitle: "Places or food styles you avoid.",
                    placeholder: "e.g. seafood restaurants, fast food...",
                    text: $profile.avoidRestaurants
                )
                questionPage(
                    tag: 5,
                    title: "Do you have any allergies?",
                    subtitle: "We'll make sure recipes never include these.",
                    placeholder: "e.g. peanuts, shellfish, gluten...",
                    text: $profile.allergies
                )
                questionPage(
                    tag: 6,
                    title: "Any textures or flavors you avoid?",
                    subtitle: "Slimy, crunchy, bitter — whatever bothers you.",
                    placeholder: "e.g. mushy vegetables, overly sweet...",
                    text: $profile.textureFlavorAvoidances
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
                        profile.save()
                        Haptics.success()
                        onComplete()
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

    // MARK: - Pages

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

    private func questionPage(
        tag: Int,
        title: String,
        subtitle: String,
        placeholder: String,
        text: Binding<String>
    ) -> some View {
        VStack(alignment: .leading, spacing: Spacing.lg) {
            Spacer()
            Text(title)
                .font(.displayTitle)
                .foregroundStyle(Color.textPrimary)
            Text(subtitle)
                .font(.dmBody)
                .foregroundStyle(.secondary)
            TextField(placeholder, text: text, axis: .vertical)
                .font(.dmBody)
                .lineLimit(3...6)
                .padding(Spacing.md)
                .background(
                    RoundedRectangle(cornerRadius: Radius.medium, style: .continuous)
                        .fill(Color.surfaceSecondary)
                )
            Text("Skip if you're not sure — you can always update in Settings.")
                .font(.dmCaption)
                .foregroundStyle(.secondary)
            Spacer()
            Spacer()
        }
        .padding(.horizontal, Spacing.xl)
        .tag(tag)
    }

    private var spicePage: some View {
        VStack(spacing: Spacing.xl) {
            Spacer()
            Text("How spicy do you like your food?")
                .font(.displayTitle)
                .foregroundStyle(Color.textPrimary)
                .multilineTextAlignment(.center)

            Text(spiceEmoji)
                .font(.system(size: 56))

            HStack(spacing: Spacing.sm) {
                ForEach(1...5, id: \.self) { level in
                    Button {
                        Haptics.select()
                        profile.spiceLevel = level
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

    private var spiceEmoji: String {
        switch profile.spiceLevel {
        case 1: return "🧊"
        case 2: return "🌱"
        case 3: return "🌶️"
        case 4: return "🔥"
        case 5: return "🌋"
        default: return "🌶️"
        }
    }
}
