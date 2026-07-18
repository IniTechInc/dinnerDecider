import SwiftUI

/// Top-level tab navigation. Each tab is its own NavigationStack so pushes stay
/// contained to that tab. A one-time onboarding cover greets first-run users.
struct RootView: View {
    @AppStorage(PrefKey.hasSeenOnboarding) private var hasSeenOnboarding = false
    @State private var showFirstRun = false
    @State private var deepLinkMealId: UUID?

    var body: some View {
        TabView {
            CaptureView()
                .tabItem { Label("Scan", systemImage: "camera") }

            InventoryView()
                .tabItem { Label("Inventory", systemImage: "list.bullet") }

            RecipesView()
                .tabItem { Label("Recipes", systemImage: "fork.knife") }

            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
        }
        .tint(.brandPrimary)
        // One presentation for the whole first run: onboarding flows straight
        // into the taste profile wizard without dismissing and re-covering.
        // Existing users are never shown either again.
        .fullScreenCover(isPresented: $showFirstRun) {
            FirstRunFlow { showFirstRun = false }
        }
        .onAppear {
            if !hasSeenOnboarding {
                showFirstRun = true
            }
        }
        .onOpenURL { url in
            handleDeepLink(url)
        }
    }

    private func handleDeepLink(_ url: URL) {
        guard url.scheme == "dinnerdecider" else { return }

        switch url.host {
        case "meal":
            // dinnerdecider://meal/{UUID}
            if let idString = url.pathComponents.dropFirst().first,
               let id = UUID(uuidString: idString) {
                deepLinkMealId = id
            }
        case "kroger-callback":
            // Handled by ASWebAuthenticationSession callback — no action needed here.
            break
        default:
            break
        }
    }
}

/// The first-run flow: onboarding slides, then the taste profile wizard, both
/// inside a single cover so the hand-off is seamless. Owns its own step state so
/// switching content never dismisses and re-presents.
private struct FirstRunFlow: View {
    var onFinished: () -> Void

    @AppStorage(PrefKey.hasSeenOnboarding) private var hasSeenOnboarding = false
    @AppStorage(PrefKey.hasCompletedTasteProfile) private var hasCompletedTasteProfile = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var step: Step = .onboarding

    private enum Step { case onboarding, tasteProfile }

    var body: some View {
        switch step {
        case .onboarding:
            OnboardingView {
                hasSeenOnboarding = true
                withMotion(reduceMotion) { step = .tasteProfile }
            }
        case .tasteProfile:
            TasteProfileWizard {
                hasCompletedTasteProfile = true
                onFinished()
            }
        }
    }
}

#Preview {
    RootView()
        .environmentObject(AppModel())
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self, PlannedMeal.self], inMemory: true)
}
