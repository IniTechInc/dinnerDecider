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
                    ProgressView()
                        .progressViewStyle(.circular)
                    Text(statusMessage)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal)
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
        isDownloading = true
        Task {
            do {
                try await downloadFile(
                    from: "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-Q4_K_M.gguf",
                    to: "gemma-4-E4B-it-Q4_K_M.gguf",
                    label: "Downloading model (2.6 GB)…"
                )
                try await downloadFile(
                    from: "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/mmproj-F16.gguf",
                    to: "mmproj-F16.gguf",
                    label: "Downloading vision projector…"
                )
                loadModel()
            } catch {
                statusMessage = "Download failed. Check connection."
                isDownloading = false
            }
        }
    }

    private func downloadFile(from urlString: String, to filename: String, label: String) async throws {
        let dest = modelsDirectory.appendingPathComponent(filename)
        guard !FileManager.default.fileExists(atPath: dest.path) else { return }
        statusMessage = label
        let (tempURL, _) = try await URLSession.shared.download(from: URL(string: urlString)!)
        try FileManager.default.moveItem(at: tempURL, to: dest)
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
