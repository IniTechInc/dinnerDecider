import SwiftUI

struct MainTabView: View {
    var body: some View {
        TabView {
            CaptureView()
                .tabItem {
                    Label("Scan", systemImage: "camera")
                }

            InventoryView()
                .tabItem {
                    Label("Pantry", systemImage: "refrigerator")
                }

            RecipesView()
                .tabItem {
                    Label("Recipes", systemImage: "fork.knife")
                }

            SettingsView()
                .tabItem {
                    Label("Settings", systemImage: "gear")
                }
        }
    }
}
