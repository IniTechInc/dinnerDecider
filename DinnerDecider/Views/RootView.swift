import SwiftUI

/// Top-level tab navigation. Each tab is its own NavigationStack so pushes stay
/// contained to that tab. A one-time onboarding cover greets first-run users.
struct RootView: View {
    @AppStorage(PrefKey.hasSeenOnboarding) private var hasSeenOnboarding = false
    @State private var showOnboarding = false

    var body: some View {
        TabView {
            // Outline symbols so iOS fills the selected tab automatically,
            // giving a consistent outlined/filled selection cue across tabs.
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
            }
        }
        .onAppear {
            if !hasSeenOnboarding {
                showOnboarding = true
            }
        }
    }
}

#Preview {
    RootView()
        .environmentObject(AppModel())
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self], inMemory: true)
}
