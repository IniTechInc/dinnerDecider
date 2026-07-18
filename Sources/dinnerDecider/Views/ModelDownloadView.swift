import SwiftUI

struct ModelDownloadView: View {
    @Binding var modelState: ModelLoadState
    @State private var downloadProgress: Double = 0
    @State private var isDownloading = false
    @State private var statusMessage = "Gemma 4 E4B (~2.6GB) is required to identify food items on-device."

    var body: some View {
        VStack(spacing: 24) {
            Spacer()

            Image(systemName: "brain.head.profile")
                .font(.system(size: 64))
                .foregroundStyle(.tint)

            VStack(spacing: 8) {
                Text("dinnerDecider")
                    .font(.largeTitle.bold())
                Text("On-device AI recipe suggestions")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }

            Text(statusMessage)
                .font(.body)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .padding(.horizontal)

            if isDownloading {
                VStack(spacing: 12) {
                    ProgressView(value: downloadProgress)
                        .progressViewStyle(.linear)
                        .padding(.horizontal)
                    Text("\(Int(downloadProgress * 100))%")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            } else {
                Button("Download Model") {
                    startDownload()
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            }

            Spacer()
        }
        .padding()
        .onAppear {
            checkForLocalModel()
        }
    }

    private func checkForLocalModel() {
        // Check if model files exist locally (bundle mode for demo)
        let fileManager = FileManager.default
        let modelURL = modelsDirectory.appendingPathComponent("gemma-4-E4B-it-Q4_K_M.gguf")
        let mmprojURL = modelsDirectory.appendingPathComponent("mmproj-F16.gguf")

        if fileManager.fileExists(atPath: modelURL.path) && fileManager.fileExists(atPath: mmprojURL.path) {
            loadModel()
        }
    }

    private func startDownload() {
        // TODO: KAN-9 — implement URLSession download with resume support
        isDownloading = true
        statusMessage = "Downloading Gemma 4 E4B..."
        // Placeholder: simulate progress
        Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { timer in
            downloadProgress += 0.01
            if downloadProgress >= 1.0 {
                timer.invalidate()
                loadModel()
            }
        }
    }

    private func loadModel() {
        modelState = .loading
    }
}

var modelsDirectory: URL {
    let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
    let dir = docs.appendingPathComponent("Models", isDirectory: true)
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir
}
