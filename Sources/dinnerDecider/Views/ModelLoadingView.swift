import SwiftUI

/// KAN-41: Shown while Gemma model is loading from disk into memory (~10-20s on device).
/// Blocks navigation to prevent the user from triggering inference before the model is ready.
struct ModelLoadingView: View {
    var body: some View {
        VStack(spacing: 20) {
            Spacer()
            ProgressView()
                .scaleEffect(1.5)
            Text("Loading Gemma 4…")
                .font(.headline)
            Text("This takes about 10–20 seconds on first launch.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Spacer()
        }
        .padding()
    }
}

struct ModelLoadErrorView: View {
    let error: Error
    @Binding var modelState: ModelLoadState

    var body: some View {
        VStack(spacing: 20) {
            Image(systemName: "exclamationmark.triangle")
                .font(.system(size: 48))
                .foregroundStyle(.red)
            Text("Failed to load model")
                .font(.headline)
            Text(error.localizedDescription)
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Button("Try Again") {
                modelState = .notLoaded
            }
            .buttonStyle(.borderedProminent)
        }
        .padding()
    }
}
