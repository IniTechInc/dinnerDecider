import SwiftUI

/// Top-level tab navigation. Each tab is its own NavigationStack so pushes stay
/// contained to that tab.
struct RootView: View {
    var body: some View {
        TabView {
            CaptureView()
                .tabItem { Label("Scan", systemImage: "camera.fill") }

            InventoryView()
                .tabItem { Label("Inventory", systemImage: "list.bullet") }

            RecipesView()
                .tabItem { Label("Recipes", systemImage: "fork.knife") }

            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape.fill") }
        }
    }
}

#Preview {
    RootView()
        .environmentObject(AppModel())
}
