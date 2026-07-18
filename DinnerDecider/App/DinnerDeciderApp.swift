import SwiftData
import SwiftUI

@main
struct DinnerDeciderApp: App {
    @StateObject private var appModel = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(appModel)
        }
        .modelContainer(for: InventoryItem.self)
    }
}
