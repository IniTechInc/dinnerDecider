import SwiftUI

struct RootView: View {
    @State private var modelState = ModelLoadState.notLoaded

    var body: some View {
        switch modelState {
        case .notLoaded:
            ModelDownloadView(modelState: $modelState)
        case .loading:
            ModelLoadingView()
                .task {
                    do {
                        try await ModelService.shared.load()
                        modelState = .ready
                    } catch {
                        modelState = .failed(error)
                    }
                }
        case .ready:
            MainTabView()
        case .failed(let error):
            ModelLoadErrorView(error: error, modelState: $modelState)
        }
    }
}

enum ModelLoadState {
    case notLoaded
    case loading
    case ready
    case failed(Error)
}
