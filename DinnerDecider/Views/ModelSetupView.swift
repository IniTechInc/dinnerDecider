import SwiftUI

struct ModelSetupView: View {
    @State private var locator = ModelFileLocator()
    @State private var isDownloading = false
    @State private var downloadStep = ""
    @State private var downloadError: String?

    var body: some View {
        List {
            Section("Status") {
                statusRow(
                    title: "Weights (.gguf)",
                    present: locator.modelURL != nil,
                    detail: locator.modelURL?.lastPathComponent
                )
                statusRow(
                    title: "Vision projector (mmproj)",
                    present: locator.mmprojURL != nil,
                    detail: locator.mmprojURL?.lastPathComponent
                )
            }

            Section {
                if isDownloading {
                    HStack(spacing: 12) {
                        ProgressView()
                        Text(downloadStep)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                } else if locator.isModelPresent {
                    Label("Model files ready", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                } else {
                    Button {
                        startDownload()
                    } label: {
                        Label("Download model (~4.5 GB)", systemImage: "arrow.down.circle")
                    }
                }
                if let error = downloadError {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            } header: {
                Text("Download")
            } footer: {
                Text("Downloads Gemma 4 E4B weights + vision projector from Hugging Face. One-time download; the app then works fully offline.")
            }

            Section {
                VStack(alignment: .leading, spacing: 8) {
                    instruction(number: 1, text: "Connect this iPhone to your Mac with a cable.")
                    instruction(number: 2, text: "Open Finder, select the iPhone, then the Files tab.")
                    instruction(number: 3, text: "Drag gemma-4-E4B-it-Q4_0.gguf and mmproj-gemma-4-E4B-it-Q8_0.gguf into DinnerDecider.")
                    instruction(number: 4, text: "Reopen this screen to confirm both files show as ready.")
                }
                .padding(.vertical, 4)
            } header: {
                Text("Copy files via Finder")
            } footer: {
                Text("File Sharing is enabled, so files dropped in via Finder are picked up automatically.")
            }
        }
        .dinnerSurfaceBackground()
        .navigationTitle("Model Setup")
        .navigationBarTitleDisplayMode(.inline)
    }

    // MARK: - Download

    private func startDownload() {
        isDownloading = true
        downloadError = nil
        Task {
            do {
                try await downloadFile(
                    urlString: "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-Q4_0.gguf",
                    filename: "gemma-4-E4B-it-Q4_0.gguf",
                    label: "Downloading weights (~4.4 GB)…"
                )
                try await downloadFile(
                    urlString: "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf",
                    filename: "mmproj-gemma-4-E4B-it-Q8_0.gguf",
                    label: "Downloading vision projector…"
                )
                locator = ModelFileLocator()
            } catch {
                downloadError = "Download failed: \(error.localizedDescription)"
            }
            isDownloading = false
        }
    }

    private func downloadFile(urlString: String, filename: String, label: String) async throws {
        guard let docsURL = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first else {
            throw URLError(.fileDoesNotExist)
        }
        let dest = docsURL.appendingPathComponent(filename)
        guard !FileManager.default.fileExists(atPath: dest.path) else { return }
        downloadStep = label
        guard let url = URL(string: urlString) else { throw URLError(.badURL) }
        let (tempURL, _) = try await URLSession.shared.download(from: url)
        try FileManager.default.moveItem(at: tempURL, to: dest)
    }

    // MARK: - Helpers

    private func statusRow(title: String, present: Bool, detail: String?) -> some View {
        HStack {
            Image(systemName: present ? "checkmark.circle.fill" : "xmark.circle")
                .foregroundStyle(present ? Color.brandSecondary : Color.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                if let detail, present {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text(present ? "Ready" : "Missing")
                .font(.caption.weight(.semibold))
                .foregroundStyle(present ? Color.brandSecondary : Color.secondary)
        }
    }

    private func instruction(number: Int, text: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Text("\(number)")
                .font(.caption.weight(.bold))
                .frame(width: 22, height: 22)
                .background(Circle().fill(.tint.opacity(0.15)))
            Text(text)
                .font(.subheadline)
        }
    }
}

#Preview {
    NavigationStack {
        ModelSetupView()
    }
}
