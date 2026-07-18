import SwiftUI

/// Top-level tab navigation. Each tab is its own NavigationStack so pushes stay
/// contained to that tab. A one-time onboarding cover greets first-run users.
struct RootView: View {
    @AppStorage(PrefKey.hasSeenOnboarding) private var hasSeenOnboarding = false
    @AppStorage(PrefKey.hasCompletedTasteProfile) private var hasCompletedTasteProfile = false
    @State private var showOnboarding = false
    @State private var showTasteWizard = false
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
        .fullScreenCover(isPresented: $showOnboarding) {
            OnboardingView {
                hasSeenOnboarding = true
                showOnboarding = false
                // After onboarding, offer the taste profile wizard.
                if !hasCompletedTasteProfile {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) {
                        showTasteWizard = true
                    }
                }
            }
        }
        .fullScreenCover(isPresented: $showTasteWizard) {
            TasteProfileWizard {
                hasCompletedTasteProfile = true
                showTasteWizard = false
            }
        }
        .onAppear {
            if !hasSeenOnboarding {
                showOnboarding = true
            } else if !hasCompletedTasteProfile {
                showTasteWizard = true
            }
        }
        .onOpenURL { url in
            handleDeepLink(url)
        }
    }

    private func handleDeepLink(_ url: URL) {
        // dinnerdecider://meal/{UUID}
        guard url.scheme == "dinnerdecider",
              url.host == "meal",
              let idString = url.pathComponents.dropFirst().first,
              let id = UUID(uuidString: idString) else { return }
        deepLinkMealId = id
    }
}

#Preview {
    RootView()
        .environmentObject(AppModel())
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self, PlannedMeal.self], inMemory: true)
}
