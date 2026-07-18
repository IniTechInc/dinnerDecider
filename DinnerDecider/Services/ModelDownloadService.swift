import Combine
import Foundation
import Network

// MARK: - Files to download

/// The two on-device model files, in download order. The small vision projector
/// downloads first so the user sees a fast, encouraging first success before the
/// large weights start.
enum ModelDownloadFile: String, CaseIterable, Identifiable {
    case mmproj
    case weights

    var id: String { rawValue }

    /// Destination filename. Must match what `ModelFileLocator` resolves so the
    /// runtime picks the files up with no further wiring.
    var fileName: String {
        switch self {
        case .mmproj: return ModelFileLocator.preferredMmprojName
        case .weights: return ModelFileLocator.defaultModelPreference[0]
        }
    }

    /// Public, no-auth direct download URL on Hugging Face.
    var remoteURL: URL {
        switch self {
        case .mmproj:
            return URL(string: "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf")!
        case .weights:
            return URL(string: "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-UD-IQ3_XXS.gguf")!
        }
    }

    /// Approximate size, used to show sensible progress before the HEAD request
    /// returns the exact Content-Length, and as a friendly label.
    var approxBytes: Int64 {
        switch self {
        case .mmproj: return 534_000_000
        case .weights: return 3_460_000_000
        }
    }

    /// Short, human name for the UI and VoiceOver.
    var displayName: String {
        switch self {
        case .mmproj: return "Vision projector"
        case .weights: return "Gemma 4 weights"
        }
    }
}

// MARK: - State

/// The download lifecycle. `failed` carries a plain-language reason for the UI.
enum ModelDownloadState: Equatable {
    case notStarted
    case downloading
    case paused
    case verifying
    case done
    case failed(reason: String)
}

/// Byte progress for a single file.
struct FileDownloadProgress: Equatable {
    var bytesReceived: Int64 = 0
    var totalBytes: Int64 = 0

    var fraction: Double {
        guard totalBytes > 0 else { return 0 }
        return min(1.0, Double(bytesReceived) / Double(totalBytes))
    }
}

// MARK: - Pure, testable logic

/// Side-effect-free decision helpers. Kept separate from the service so the
/// gates and the state machine can be unit tested without touching URLSession,
/// the network, or the filesystem.
enum ModelDownloadLogic {

    /// Free space we insist on before starting. Weights + projector are ~4.0 GB;
    /// 4.5 GB leaves headroom for the temp copy during the atomic move.
    static let requiredFreeBytes: Int64 = 4_500_000_000

    /// Whether there is enough free disk to safely download.
    static func hasEnoughDiskSpace(freeBytes: Int64, required: Int64 = requiredFreeBytes) -> Bool {
        freeBytes >= required
    }

    /// Whether a finished file's real size matches the server's Content-Length.
    /// A zero (unknown) expected size is treated as invalid so we never accept a
    /// file we cannot vouch for.
    static func sizeIsValid(actual: Int64, expected: Int64) -> Bool {
        expected > 0 && actual == expected
    }

    /// Whether a download may begin given the network. On cellular we require an
    /// explicit user confirmation first.
    static func mayStartDownload(isCellular: Bool, userConfirmedCellular: Bool) -> Bool {
        if isCellular && !userConfirmedCellular { return false }
        return true
    }

    /// Overall completion fraction from summed bytes.
    static func overallFraction(received: Int64, total: Int64) -> Double {
        guard total > 0 else { return 0 }
        return min(1.0, Double(received) / Double(total))
    }

    /// Events that drive the state machine.
    enum Event: Equatable {
        case start
        case pause
        case resume
        case allDownloaded
        case verifiedOK
        case verificationFailed(String)
        case failed(String)
        case retry
    }

    /// Pure state transition. Any unhandled pairing is a no-op (returns the
    /// current state), which keeps stray delegate callbacks from corrupting UI.
    static func next(_ state: ModelDownloadState, _ event: Event) -> ModelDownloadState {
        switch (state, event) {
        case (_, .failed(let reason)):
            return .failed(reason: reason)
        case (.notStarted, .start),
             (.failed, .retry),
             (.failed, .start),
             (.done, .start):
            return .downloading
        case (.downloading, .pause):
            return .paused
        case (.paused, .resume), (.paused, .start):
            return .downloading
        case (.downloading, .allDownloaded):
            return .verifying
        case (.verifying, .verifiedOK):
            return .done
        case (.verifying, .verificationFailed(let reason)):
            return .failed(reason: reason)
        default:
            return state
        }
    }
}

// MARK: - Notifications

extension Notification.Name {
    /// Posted on the main thread when both files are downloaded and verified so
    /// the rest of the app can flip to the model-ready state.
    static let modelDownloadDidComplete = Notification.Name("modelDownloadDidComplete")
}

// MARK: - Service

/// Downloads the on-device model files with a background `URLSession` so
/// transfers continue while the app is suspended, survive relaunch, and can be
/// paused/resumed. Consumed directly by `ModelSetupView`.
@MainActor
final class ModelDownloadService: NSObject, ObservableObject {

    /// Fixed identifier so the same background session is recreated across
    /// launches and reattaches to any in-flight tasks.
    static let backgroundSessionIdentifier = "com.philwoolley.dinnerdecider.modeldownload"

    /// Shared instance. A singleton is required so the `AppDelegate` background
    /// event callback routes to the same session the UI is observing.
    static let shared = ModelDownloadService()

    // Published UI state
    @Published private(set) var state: ModelDownloadState = .notStarted
    @Published private(set) var progress: [ModelDownloadFile: FileDownloadProgress] = [:]
    @Published private(set) var currentFile: ModelDownloadFile?
    @Published private(set) var bytesPerSecond: Double = 0
    @Published private(set) var isCellular = false
    @Published private(set) var isExpensive = false
    /// Set when a start was blocked because the user is on cellular; the UI shows
    /// a confirmation and calls `start(overCellularConfirmed: true)`.
    @Published var needsCellularConfirmation = false

    private var session: URLSession!
    private let pathMonitor = NWPathMonitor()
    private let monitorQueue = DispatchQueue(label: "modeldownload.path")

    private var resumeData: [ModelDownloadFile: Data] = [:]
    private var activeTasks: [ModelDownloadFile: URLSessionDownloadTask] = [:]
    private var overCellularConfirmed = false

    // Speed estimate (exponential smoothing).
    private var lastSampleTime: Date?
    private var lastSampleBytes: Int64 = 0

    private let fileManager = FileManager.default
    private let defaults = UserDefaults.standard

    private override init() {
        super.init()
        let config = URLSessionConfiguration.background(withIdentifier: Self.backgroundSessionIdentifier)
        config.isDiscretionary = false
        config.sessionSendsLaunchEvents = true
        config.allowsCellularAccess = true // gated per-request instead
        config.waitsForConnectivity = true
        session = URLSession(configuration: config, delegate: self, delegateQueue: nil)

        for file in ModelDownloadFile.allCases {
            var fileProgress = FileDownloadProgress()
            fileProgress.totalBytes = expectedSize(for: file) ?? file.approxBytes
            if isPresent(file) {
                fileProgress.bytesReceived = fileProgress.totalBytes
            }
            progress[file] = fileProgress
        }
        if allFilesPresent {
            state = .done
        }

        startPathMonitor()
        reattachToBackgroundTasks()
    }

    // MARK: Derived UI values

    /// Combined byte progress across both files.
    var overallProgress: FileDownloadProgress {
        var received: Int64 = 0
        var total: Int64 = 0
        for file in ModelDownloadFile.allCases {
            let fileProgress = progress[file] ?? FileDownloadProgress()
            received += fileProgress.bytesReceived
            total += fileProgress.totalBytes > 0 ? fileProgress.totalBytes : file.approxBytes
        }
        return FileDownloadProgress(bytesReceived: received, totalBytes: total)
    }

    // MARK: Public control

    /// Begin (or restart) the download. Runs preflight checks, then downloads any
    /// missing files sequentially, projector first.
    func start(overCellularConfirmed: Bool = false) {
        self.overCellularConfirmed = overCellularConfirmed
        needsCellularConfirmation = false

        if allFilesPresent {
            state = .done
            return
        }

        if let free = freeDiskBytes(), !ModelDownloadLogic.hasEnoughDiskSpace(freeBytes: free) {
            let needGB = Double(ModelDownloadLogic.requiredFreeBytes) / 1_000_000_000
            state = .failed(reason: "Not enough free space. Please free up about \(String(format: "%.1f", needGB)) GB and try again.")
            return
        }

        guard ModelDownloadLogic.mayStartDownload(isCellular: isCellular, userConfirmedCellular: overCellularConfirmed) else {
            needsCellularConfirmation = true
            return
        }

        state = .downloading
        Task { await fetchExpectedSizesThenStart() }
    }

    /// Pause the in-flight file, keeping resume data so it continues where it
    /// left off.
    func pause() {
        guard state == .downloading, let file = currentFile, let task = activeTasks[file] else { return }
        task.cancel(byProducingResumeData: { [weak self] data in
            Task { @MainActor in
                guard let self else { return }
                if let data { self.resumeData[file] = data }
                self.activeTasks[file] = nil
                self.state = .paused
                self.bytesPerSecond = 0
            }
        })
    }

    /// Resume a paused download.
    func resume() {
        guard state == .paused else { return }
        state = .downloading
        startNextDownload()
    }

    /// Cancel everything and return to the not-started state. Keeps any already
    /// completed file on disk; only clears the file still in flight.
    func cancel() {
        for (_, task) in activeTasks { task.cancel() }
        activeTasks.removeAll()
        resumeData.removeAll()
        bytesPerSecond = 0
        currentFile = nil
        state = .notStarted
        for file in ModelDownloadFile.allCases where !isPresent(file) {
            progress[file]?.bytesReceived = 0
        }
    }

    /// Retry after a failure (partial/corrupt files were already removed).
    func retry() {
        state = .notStarted
        start(overCellularConfirmed: overCellularConfirmed)
    }

    /// Re-check the filesystem (e.g. after files were copied in via Finder) and
    /// flip to done if both are now present.
    func refreshPresence() {
        for file in ModelDownloadFile.allCases where isPresent(file) {
            let total = progress[file]?.totalBytes ?? file.approxBytes
            progress[file]?.bytesReceived = total
        }
        if allFilesPresent, state != .downloading {
            state = .done
        }
    }

    // MARK: Preflight helpers

    private func freeDiskBytes() -> Int64? {
        if let values = try? documentsURL.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey]),
           let capacity = values.volumeAvailableCapacityForImportantUsage {
            return Int64(capacity)
        }
        return nil
    }

    private func fetchExpectedSizesThenStart() async {
        await fetchExpectedSizes()
        startNextDownload()
    }

    private func fetchExpectedSizes() async {
        for file in ModelDownloadFile.allCases where expectedSize(for: file) == nil {
            if let size = await headContentLength(file.remoteURL), size > 0 {
                defaults.set(NSNumber(value: size), forKey: expectedSizeKey(file))
                progress[file]?.totalBytes = size
            }
        }
    }

    private func headContentLength(_ url: URL) async -> Int64? {
        var request = URLRequest(url: url)
        request.httpMethod = "HEAD"
        request.allowsCellularAccess = overCellularConfirmed || !isCellular
        do {
            let (_, response) = try await URLSession.shared.data(for: request)
            if let http = response as? HTTPURLResponse, http.expectedContentLength > 0 {
                return http.expectedContentLength
            }
        } catch {
            // Fall back to approximate sizing; verification still guards us if a
            // Content-Length becomes available later.
        }
        return nil
    }

    // MARK: Sequencing

    private func startNextDownload() {
        guard state == .downloading else { return }

        guard let file = ModelDownloadFile.allCases.first(where: { !isPresent($0) }) else {
            verifyAll()
            return
        }

        currentFile = file
        resetSpeedSample()

        let task: URLSessionDownloadTask
        if let data = resumeData[file] {
            task = session.downloadTask(withResumeData: data)
            resumeData[file] = nil
        } else {
            var request = URLRequest(url: file.remoteURL)
            request.allowsCellularAccess = overCellularConfirmed || !isCellular
            request.allowsExpensiveNetworkAccess = overCellularConfirmed
            task = session.downloadTask(with: request)
        }
        task.taskDescription = file.rawValue
        activeTasks[file] = task
        task.resume()
    }

    private func verifyAll() {
        state = .verifying
        for file in ModelDownloadFile.allCases {
            let url = destinationURL(for: file)
            let actual = fileSize(at: url)
            if let expected = expectedSize(for: file) {
                if !ModelDownloadLogic.sizeIsValid(actual: actual, expected: expected) {
                    try? fileManager.removeItem(at: url)
                    progress[file]?.bytesReceived = 0
                    state = .failed(reason: "\(file.displayName) looked incomplete, so it was removed. Tap Retry to finish the download.")
                    return
                }
            } else if actual <= 0 {
                state = .failed(reason: "\(file.displayName) came through empty. Tap Retry to try again.")
                return
            }
        }
        state = .done
        NotificationCenter.default.post(name: .modelDownloadDidComplete, object: nil)
    }

    // MARK: State applied from background callbacks

    fileprivate func updateProgress(file: ModelDownloadFile, received: Int64, expected: Int64) {
        var fileProgress = progress[file] ?? FileDownloadProgress()
        fileProgress.bytesReceived = received
        if expected > 0 {
            fileProgress.totalBytes = expected
        } else if let stored = expectedSize(for: file) {
            fileProgress.totalBytes = stored
        } else if fileProgress.totalBytes == 0 {
            fileProgress.totalBytes = file.approxBytes
        }
        progress[file] = fileProgress
        currentFile = file
        if state != .downloading { state = .downloading }
        updateSpeed(received: received)
    }

    fileprivate func handleFinishedDownload(file: ModelDownloadFile, tmpSize: Int64, sizeOK: Bool, moveError: String?) {
        activeTasks[file] = nil
        if let moveError {
            state = .failed(reason: "Could not save \(file.displayName): \(moveError). Tap Retry.")
            return
        }
        if !sizeOK {
            progress[file]?.bytesReceived = 0
            state = .failed(reason: "\(file.displayName) downloaded incompletely and was removed. Tap Retry to finish.")
            return
        }
        let total = progress[file]?.totalBytes ?? tmpSize
        progress[file]?.bytesReceived = total
        resetSpeedSample()

        if allFilesPresent {
            verifyAll()
        } else {
            startNextDownload()
        }
    }

    fileprivate func handleTaskError(file: ModelDownloadFile?, error: NSError, resumeData data: Data?) {
        if let file { activeTasks[file] = nil }
        if error.code == NSURLErrorCancelled {
            // Intentional pause/cancel. Keep resume data only if we are paused.
            if state == .paused, let file, let data { resumeData[file] = data }
            return
        }
        if let file, let data { resumeData[file] = data }
        bytesPerSecond = 0
        state = .failed(reason: friendlyMessage(for: error))
    }

    private func friendlyMessage(for error: NSError) -> String {
        switch error.code {
        case NSURLErrorNotConnectedToInternet, NSURLErrorNetworkConnectionLost:
            return "The internet connection dropped. Reconnect to Wi-Fi and tap Retry."
        case NSURLErrorTimedOut:
            return "The download timed out. Check your connection and tap Retry."
        case NSURLErrorCannotConnectToHost, NSURLErrorCannotFindHost, NSURLErrorDNSLookupFailed:
            return "Could not reach the download server. Try again in a moment."
        default:
            return "The download stopped: \(error.localizedDescription). Tap Retry."
        }
    }

    // MARK: Speed

    private func updateSpeed(received: Int64) {
        let now = Date()
        guard let last = lastSampleTime else {
            lastSampleTime = now
            lastSampleBytes = received
            return
        }
        let elapsed = now.timeIntervalSince(last)
        guard elapsed >= 0.5 else { return }
        let deltaBytes = Double(received - lastSampleBytes)
        let instant = max(0, deltaBytes / elapsed)
        bytesPerSecond = bytesPerSecond == 0 ? instant : (bytesPerSecond * 0.7 + instant * 0.3)
        lastSampleTime = now
        lastSampleBytes = received
    }

    private func resetSpeedSample() {
        lastSampleTime = nil
        lastSampleBytes = 0
        bytesPerSecond = 0
    }

    // MARK: Network monitor

    private func startPathMonitor() {
        pathMonitor.pathUpdateHandler = { [weak self] path in
            let cellular = path.usesInterfaceType(.cellular)
            let expensive = path.isExpensive
            Task { @MainActor in
                self?.isCellular = cellular
                self?.isExpensive = expensive
            }
        }
        pathMonitor.start(queue: monitorQueue)
    }

    // MARK: Reattach

    private func reattachToBackgroundTasks() {
        session.getAllTasks { [weak self] tasks in
            let downloads = tasks.compactMap { $0 as? URLSessionDownloadTask }
            Task { @MainActor in
                guard let self else { return }
                for task in downloads {
                    guard let file = self.file(for: task) else { continue }
                    self.activeTasks[file] = task
                    if task.state == .running || task.state == .suspended {
                        self.currentFile = file
                        if self.state == .notStarted || self.state == .paused {
                            self.state = .downloading
                        }
                    }
                }
            }
        }
    }

    // MARK: Filesystem helpers

    private var documentsURL: URL {
        fileManager.urls(for: .documentDirectory, in: .userDomainMask)[0]
    }

    private func destinationURL(for file: ModelDownloadFile) -> URL {
        documentsURL.appendingPathComponent(file.fileName)
    }

    private func fileSize(at url: URL) -> Int64 {
        guard let attrs = try? fileManager.attributesOfItem(atPath: url.path),
              let size = attrs[.size] as? NSNumber else { return 0 }
        return size.int64Value
    }

    private func isPresent(_ file: ModelDownloadFile) -> Bool {
        fileSize(at: destinationURL(for: file)) > 0
    }

    private var allFilesPresent: Bool {
        ModelDownloadFile.allCases.allSatisfy { isPresent($0) }
    }

    private func expectedSizeKey(_ file: ModelDownloadFile) -> String {
        "modeldownload.size.\(file.fileName)"
    }

    private func expectedSize(for file: ModelDownloadFile) -> Int64? {
        (defaults.object(forKey: expectedSizeKey(file)) as? NSNumber)?.int64Value
    }

    // MARK: Nonisolated helpers (safe from any thread)

    nonisolated fileprivate func file(for task: URLSessionTask) -> ModelDownloadFile? {
        if let description = task.taskDescription, let file = ModelDownloadFile(rawValue: description) {
            return file
        }
        if let url = task.originalRequest?.url {
            return ModelDownloadFile.allCases.first { $0.remoteURL == url }
        }
        return nil
    }

    nonisolated fileprivate func expectedSizeNonisolated(_ file: ModelDownloadFile) -> Int64? {
        (UserDefaults.standard.object(forKey: "modeldownload.size.\(file.fileName)") as? NSNumber)?.int64Value
    }
}

// MARK: - URLSession delegate

extension ModelDownloadService: URLSessionDownloadDelegate {

    nonisolated func urlSession(
        _ session: URLSession,
        downloadTask: URLSessionDownloadTask,
        didWriteData bytesWritten: Int64,
        totalBytesWritten: Int64,
        totalBytesExpectedToWrite: Int64
    ) {
        guard let file = file(for: downloadTask) else { return }
        Task { @MainActor in
            self.updateProgress(file: file, received: totalBytesWritten, expected: totalBytesExpectedToWrite)
        }
    }

    nonisolated func urlSession(
        _ session: URLSession,
        downloadTask: URLSessionDownloadTask,
        didFinishDownloadingTo location: URL
    ) {
        guard let file = file(for: downloadTask) else { return }

        // The temp file is only valid during this callback, so do the size check
        // and atomic move synchronously here, then hop to the main actor to
        // update state.
        let fm = FileManager.default
        let dest = fm.urls(for: .documentDirectory, in: .userDomainMask)[0]
            .appendingPathComponent(file.fileName)

        let tmpSize = (try? fm.attributesOfItem(atPath: location.path)[.size] as? NSNumber)??.int64Value ?? 0
        let expected = expectedSizeNonisolated(file)

        var sizeOK = true
        if let expected, expected > 0, tmpSize != expected {
            sizeOK = false
        } else if expected == nil, tmpSize <= 0 {
            sizeOK = false
        }

        var moveError: String?
        if sizeOK {
            do {
                if fm.fileExists(atPath: dest.path) {
                    try fm.removeItem(at: dest)
                }
                try fm.moveItem(at: location, to: dest)
            } catch {
                moveError = error.localizedDescription
            }
        } else {
            try? fm.removeItem(at: location)
        }

        Task { @MainActor in
            self.handleFinishedDownload(file: file, tmpSize: tmpSize, sizeOK: sizeOK, moveError: moveError)
        }
    }

    nonisolated func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didCompleteWithError error: Error?
    ) {
        guard let error = error as NSError? else { return } // success handled above
        let file = file(for: task)
        let data = error.userInfo[NSURLSessionDownloadTaskResumeData] as? Data
        Task { @MainActor in
            self.handleTaskError(file: file, error: error, resumeData: data)
        }
    }

    nonisolated func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        Task { @MainActor in
            let handler = AppDelegate.backgroundCompletionHandler
            AppDelegate.backgroundCompletionHandler = nil
            handler?()
        }
    }
}
