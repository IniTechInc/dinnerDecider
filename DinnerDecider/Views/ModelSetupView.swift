import SwiftUI

/// Shows on-device model status and how to install the files. The download path
/// is a placeholder for now; copying files in via Finder works today.
struct ModelSetupView: View {
    private let locator = ModelFileLocator()

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
                Button {
                    // Placeholder: background download from Hugging Face will be
                    // wired in with the real runtime.
                } label: {
                    Label("Download model (coming soon)", systemImage: "arrow.down.circle")
                }
                .disabled(true)
            } header: {
                Text("Download")
            } footer: {
                Text("A one-time download from Hugging Face will land here. After that the app works fully offline.")
            }

            Section {
                VStack(alignment: .leading, spacing: 8) {
                    instruction(number: 1, text: "Connect this iPhone to your Mac with a cable.")
                    instruction(number: 2, text: "Open Finder, select the iPhone, then the Files tab.")
                    instruction(number: 3, text: "Drag the .gguf weights and mmproj-F16.gguf into DinnerDecider.")
                    instruction(number: 4, text: "Reopen this screen to confirm both files show as ready.")
                }
                .padding(.vertical, 4)
            } header: {
                Text("Copy files via Finder")
            } footer: {
                Text("File Sharing is enabled, so model files dropped in via Finder are picked up automatically.")
            }
        }
        .navigationTitle("Model Setup")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func statusRow(title: String, present: Bool, detail: String?) -> some View {
        HStack {
            Image(systemName: present ? "checkmark.circle.fill" : "xmark.circle")
                .foregroundStyle(present ? .green : .secondary)
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
                .foregroundStyle(present ? .green : .secondary)
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
