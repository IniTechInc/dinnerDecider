import SwiftUI

/// On-device model setup. Downloads the Gemma 4 weights and vision projector
/// directly on the device (no computer needed), with progress, pause/resume,
/// integrity checks, and a plain-language error path. Copying files in via a
/// computer stays available under an "Advanced" section.
struct ModelSetupView: View {
    @ObservedObject private var download = ModelDownloadService.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @State private var showCellularConfirm = false

    var body: some View {
        ScrollView {
            VStack(spacing: Spacing.lg) {
                switch download.state {
                case .notStarted:
                    noModelCard
                case .downloading, .paused:
                    downloadingCard
                case .verifying:
                    verifyingCard
                case .done:
                    readyCard
                case .failed(let reason):
                    failedCard(reason: reason)
                }

                advancedSection
            }
            .padding(Spacing.lg)
        }
        .dinnerSurfaceBackground()
        .navigationTitle("Model Setup")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { download.refreshPresence() }
        .onChange(of: download.needsCellularConfirmation) { _, needs in
            showCellularConfirm = needs
        }
        .alert("You are on cellular", isPresented: $showCellularConfirm) {
            Button("Wait for Wi-Fi", role: .cancel) {
                download.needsCellularConfirmation = false
            }
            Button("Download over cellular anyway") {
                download.start(overCellularConfirmed: true)
            }
        } message: {
            Text("This download is about 4 GB. On cellular it may be slow and use a lot of data. Wi-Fi is recommended.")
        }
    }

    // MARK: No model

    private var noModelCard: some View {
        Card {
            VStack(spacing: Spacing.md) {
                Image(systemName: "brain.head.profile")
                    .font(.system(size: 44))
                    .foregroundStyle(Color.brandSecondary)
                    .accessibilityHidden(true)

                Text("Download the chef's brain")
                    .font(.displayTitle)
                    .foregroundStyle(Color.textPrimary)
                    .multilineTextAlignment(.center)

                Text("About 4 GB, best on Wi-Fi. After this one-time download the app identifies your food fully on-device, offline and private.")
                    .font(.dmBody)
                    .foregroundStyle(Color.textSecondary)
                    .multilineTextAlignment(.center)

                VStack(spacing: Spacing.sm) {
                    ForEach(ModelDownloadFile.allCases) { file in
                        fileSizeRow(file)
                    }
                }
                .padding(.top, Spacing.xs)

                Button {
                    download.start()
                } label: {
                    Label("Download", systemImage: "arrow.down.circle.fill")
                        .font(.dmBodyBold)
                        .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.borderedProminent)
                .tint(Color.brandPrimary)
                .accessibilityLabel("Download the on-device model, about 4 gigabytes")

                Label("Best on Wi-Fi", systemImage: "wifi")
                    .font(.dmFootnote)
                    .foregroundStyle(Color.textSecondary)
                    .accessibilityLabel("Best downloaded on Wi-Fi")
            }
        }
    }

    private func fileSizeRow(_ file: ModelDownloadFile) -> some View {
        HStack {
            Text(file.displayName)
                .font(.dmSubheadline)
                .foregroundStyle(Color.textPrimary)
            Spacer()
            Text(formatBytes(file.approxBytes))
                .font(.dmFootnote)
                .foregroundStyle(Color.textSecondary)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(file.displayName), about \(formatBytes(file.approxBytes))")
    }

    // MARK: Downloading

    private var downloadingCard: some View {
        let overall = download.overallProgress
        let isPaused = download.state == .paused
        return Card {
            VStack(alignment: .leading, spacing: Spacing.md) {
                HStack {
                    Text(isPaused ? "Paused" : "Downloading")
                        .font(.dmHeadline)
                        .foregroundStyle(Color.textPrimary)
                    Spacer()
                    Text("\(Int((overall.fraction * 100).rounded()))%")
                        .font(.dmHeadline.monospacedDigit())
                        .foregroundStyle(Color.brandPrimary)
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel("\(isPaused ? "Paused at" : "Downloading,") \(Int((overall.fraction * 100).rounded())) percent")

                ProgressView(value: overall.fraction)
                    .tint(Color.brandPrimary)
                    .animation(reduceMotion ? nil : .default, value: overall.fraction)

                Text("\(formatBytes(overall.bytesReceived)) of \(formatBytes(overall.totalBytes))\(speedSuffix(isPaused: isPaused))")
                    .font(.dmFootnote.monospacedDigit())
                    .foregroundStyle(Color.textSecondary)

                Divider().overlay(Color.separatorNeutral)

                ForEach(ModelDownloadFile.allCases) { file in
                    fileProgressRow(file)
                }

                Label("Keeps going in the background. You can leave this screen or lock your phone.", systemImage: "moon.zzz")
                    .font(.dmFootnote)
                    .foregroundStyle(Color.textSecondary)
                    .padding(.top, Spacing.xs)

                HStack(spacing: Spacing.md) {
                    if isPaused {
                        Button {
                            download.resume()
                        } label: {
                            Label("Resume", systemImage: "play.fill")
                                .font(.dmBodyBold)
                                .frame(maxWidth: .infinity, minHeight: 44)
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Color.brandPrimary)
                    } else {
                        Button {
                            download.pause()
                        } label: {
                            Label("Pause", systemImage: "pause.fill")
                                .font(.dmBodyBold)
                                .frame(maxWidth: .infinity, minHeight: 44)
                        }
                        .buttonStyle(.bordered)
                        .tint(Color.brandPrimary)
                    }

                    Button(role: .destructive) {
                        download.cancel()
                    } label: {
                        Text("Cancel")
                            .font(.dmBodyBold)
                            .frame(maxWidth: .infinity, minHeight: 44)
                    }
                    .buttonStyle(.bordered)
                }
                .padding(.top, Spacing.xs)
            }
        }
    }

    private func fileProgressRow(_ file: ModelDownloadFile) -> some View {
        let fileProgress = download.progress[file] ?? FileDownloadProgress()
        let isCurrent = download.currentFile == file
        let complete = fileProgress.totalBytes > 0 && fileProgress.bytesReceived >= fileProgress.totalBytes
        return VStack(alignment: .leading, spacing: Spacing.xs) {
            HStack {
                Image(systemName: complete ? "checkmark.circle.fill" : (isCurrent ? "arrow.down.circle" : "circle"))
                    .foregroundStyle(complete ? Color.brandSecondary : Color.textSecondary)
                    .accessibilityHidden(true)
                Text(file.displayName)
                    .font(.dmSubheadline)
                    .foregroundStyle(Color.textPrimary)
                Spacer()
                Text("\(Int((fileProgress.fraction * 100).rounded()))%")
                    .font(.dmFootnote.monospacedDigit())
                    .foregroundStyle(Color.textSecondary)
            }
            ProgressView(value: fileProgress.fraction)
                .tint(complete ? Color.brandSecondary : Color.brandPrimary)
                .animation(reduceMotion ? nil : .default, value: fileProgress.fraction)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(file.displayName), \(Int((fileProgress.fraction * 100).rounded())) percent\(complete ? ", complete" : "")")
    }

    // MARK: Verifying

    private var verifyingCard: some View {
        Card {
            VStack(spacing: Spacing.md) {
                ProgressView()
                    .accessibilityHidden(true)
                Text("Checking the download")
                    .font(.dmHeadline)
                    .foregroundStyle(Color.textPrimary)
                Text("Making sure both files arrived complete. This only takes a moment.")
                    .font(.dmFootnote)
                    .foregroundStyle(Color.textSecondary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Checking the download, please wait")
    }

    // MARK: Ready

    private var readyCard: some View {
        Card {
            VStack(spacing: Spacing.md) {
                Image(systemName: "checkmark.seal.fill")
                    .font(.system(size: 44))
                    .foregroundStyle(Color.brandSecondary)
                    .accessibilityHidden(true)
                Text("On-device Gemma 4 ready")
                    .font(.displayTitle)
                    .foregroundStyle(Color.textPrimary)
                    .multilineTextAlignment(.center)
                Text("Everything runs privately on your iPhone, offline. No account, no cloud.")
                    .font(.dmBody)
                    .foregroundStyle(Color.textSecondary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("On-device Gemma 4 is ready. Everything runs privately on your iPhone.")
    }

    // MARK: Failed

    private func failedCard(reason: String) -> some View {
        Card {
            VStack(spacing: Spacing.md) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.system(size: 40))
                    .foregroundStyle(Color.brandAccent)
                    .accessibilityHidden(true)
                Text("Download didn't finish")
                    .font(.dmHeadline)
                    .foregroundStyle(Color.textPrimary)
                Text(reason)
                    .font(.dmBody)
                    .foregroundStyle(Color.textSecondary)
                    .multilineTextAlignment(.center)
                Button {
                    download.retry()
                } label: {
                    Label("Retry", systemImage: "arrow.clockwise")
                        .font(.dmBodyBold)
                        .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.borderedProminent)
                .tint(Color.brandPrimary)
            }
            .frame(maxWidth: .infinity)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Download did not finish. \(reason)")
    }

    // MARK: Advanced (copy from a computer)

    private var advancedSection: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                instruction(number: 1, text: "Connect this iPhone to your Mac with a cable.")
                instruction(number: 2, text: "Open Finder, select the iPhone, then the Files tab.")
                instruction(number: 3, text: "Drag the .gguf weights and the mmproj file into DinnerDecider.")
                instruction(number: 4, text: "Reopen this screen to confirm both files show as ready.")
                Text("File Sharing is enabled, so model files dropped in via Finder are picked up automatically.")
                    .font(.dmFootnote)
                    .foregroundStyle(Color.textSecondary)
                    .padding(.top, Spacing.xs)
            }
            .padding(.top, Spacing.sm)
        } label: {
            Label("Advanced: copy from a computer", systemImage: "desktopcomputer")
                .font(.dmSubheadline)
                .foregroundStyle(Color.textPrimary)
        }
        .tint(Color.brandPrimary)
        .padding(Spacing.lg)
        .background(
            RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
                .fill(Color.surfaceSecondary)
        )
    }

    private func instruction(number: Int, text: String) -> some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Text("\(number)")
                .font(.dmCaption.weight(.bold))
                .frame(minWidth: 24, minHeight: 24)
                .background(Circle().fill(Color.brandPrimary.opacity(0.15)))
                .foregroundStyle(Color.brandPrimary)
            Text(text)
                .font(.dmSubheadline)
                .foregroundStyle(Color.textPrimary)
        }
    }

    // MARK: Formatting

    private func speedSuffix(isPaused: Bool) -> String {
        guard !isPaused, download.bytesPerSecond > 0 else { return "" }
        return "  \u{2022}  \(formatBytes(Int64(download.bytesPerSecond)))/s"
    }

    private func formatBytes(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }
}

/// A rounded surface card matching the brand's warm, organic direction.
private struct Card<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        content
            .padding(Spacing.lg)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                RoundedRectangle(cornerRadius: Radius.large, style: .continuous)
                    .fill(Color.surfaceSecondary)
            )
    }
}

#Preview {
    NavigationStack {
        ModelSetupView()
    }
}
