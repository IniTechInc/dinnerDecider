import SwiftData
import SwiftUI

@main
struct DinnerDeciderApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var appModel = AppModel()

    init() {
        AppTheme.configureNavigationBar()
    }

    var body: some Scene {
        WindowGroup {
            if SelfTest.isRequested {
                // Headless memory self-test path (launched with --llm-selftest).
                // Skips the normal UI so the orchestrator can reproduce a scan's
                // memory behaviour on device without any taps.
                SelfTestView()
            } else {
                RootView()
                    .environmentObject(appModel)
            }
        }
        .modelContainer(for: [InventoryItem.self, ShoppingListItem.self])
    }
}

/// Minimal screen shown only during `--llm-selftest`: runs the self-test on
/// appear, then displays its result line (also written to Documents and NSLog).
private struct SelfTestView: View {
    @State private var result = "Running self-test…"

    var body: some View {
        VStack(spacing: 16) {
            ProgressView()
            Text("LLM self-test")
                .font(.headline)
            Text(result)
                .font(.footnote.monospaced())
                .multilineTextAlignment(.leading)
                .padding()
                .textSelection(.enabled)
        }
        .padding()
        .task {
            result = await SelfTest.run()
        }
    }
}
