import AuthenticationServices
import SwiftUI

/// Presents Kroger's OAuth login in an ASWebAuthenticationSession.
struct KrogerAuthSheet: View {
    @ObservedObject var kroger: KrogerService
    @Environment(\.dismiss) private var dismiss
    @State private var authError: String?
    @State private var isAuthenticating = false

    var body: some View {
        NavigationStack {
            VStack(spacing: Spacing.xl) {
                Spacer()
                Image(systemName: "cart.circle.fill")
                    .font(.system(size: 72))
                    .foregroundStyle(Color.brandPrimary)
                Text("Connect to Kroger")
                    .font(.displayTitle)
                    .foregroundStyle(Color.textPrimary)
                Text("Sign in to your Kroger account to send shopping lists directly to your cart.")
                    .font(.dmBody)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, Spacing.xl)

                if let error = authError {
                    Text(error)
                        .font(.dmCaption)
                        .foregroundStyle(.red)
                        .padding(.horizontal, Spacing.xl)
                }

                Button("Sign in with Kroger") {
                    startAuth()
                }
                .font(.dmBodyBold)
                .buttonStyle(.borderedProminent)
                .tint(.brandPrimary)
                .disabled(isAuthenticating)

                Spacer()
                Spacer()
            }
            .padding()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private func startAuth() {
        Task {
            guard let authURL = await kroger.connect() else {
                authError = "Could not build authorization URL."
                return
            }

            isAuthenticating = true
            authError = nil

            do {
                let callbackURL = try await authenticate(url: authURL)
                let success = await kroger.handleCallback(callbackURL)
                if success {
                    dismiss()
                } else {
                    authError = "Failed to complete sign-in. Please try again."
                }
            } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
                // User tapped cancel — just stay on this sheet
            } catch {
                authError = "Sign-in was interrupted: \(error.localizedDescription)"
            }

            isAuthenticating = false
        }
    }

    @MainActor
    private func authenticate(url: URL) async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            let session = ASWebAuthenticationSession(
                url: url,
                callback: .customScheme("dinnerdecider")
            ) { callbackURL, error in
                if let error {
                    continuation.resume(throwing: error)
                } else if let callbackURL {
                    continuation.resume(returning: callbackURL)
                } else {
                    continuation.resume(throwing: URLError(.cancelled))
                }
            }
            session.presentationContextProvider = PresentationContextProvider.shared
            session.prefersEphemeralWebBrowserSession = false
            session.start()
        }
    }
}

/// Provides the key window as the presentation anchor for ASWebAuthenticationSession.
private final class PresentationContextProvider: NSObject, ASWebAuthenticationPresentationContextProviding {
    static let shared = PresentationContextProvider()

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        guard let scene = UIApplication.shared.connectedScenes.first(where: { $0.activationState == .foregroundActive }) as? UIWindowScene,
              let window = scene.windows.first(where: \.isKeyWindow)
        else {
            return ASPresentationAnchor()
        }
        return window
    }
}
