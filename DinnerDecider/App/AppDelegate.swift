import UIKit

/// Minimal app delegate whose only job is to catch background `URLSession`
/// completion events. When the system finishes a background download while the
/// app is suspended, it relaunches the app just long enough to hand us this
/// completion handler; we stash it and call it once the session delegate has
/// drained its events. Wired in via `@UIApplicationDelegateAdaptor` in
/// `DinnerDeciderApp`.
@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {

    /// The system-provided handler to call when background session events are
    /// done. Read and cleared in `urlSessionDidFinishEvents`.
    static var backgroundCompletionHandler: (() -> Void)?

    func application(
        _ application: UIApplication,
        handleEventsForBackgroundURLSession identifier: String,
        completionHandler: @escaping () -> Void
    ) {
        if identifier == ModelDownloadService.backgroundSessionIdentifier {
            AppDelegate.backgroundCompletionHandler = completionHandler
            // Touch the shared service so its background URLSession is recreated
            // and reattaches to the in-flight tasks, delivering their events.
            _ = ModelDownloadService.shared
        } else {
            completionHandler()
        }
    }
}
