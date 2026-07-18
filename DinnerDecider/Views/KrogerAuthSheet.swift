import AuthenticationServices
import SwiftUI

/// Presents Kroger's OAuth login in an ASWebAuthenticationSession.
struct KrogerAuthSheet: View {
    @ObservedObject var kroger: KrogerService
    @Environment(\.dismiss) private var dismiss
    @State private var authError: String?

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

            // Use ASWebAuthenticationSession via the continuation-based API
            do {
                let callbackURL = try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<URL, Error>) in
                    let session = ASWebAuthenticationSession(
                        url: authURL,
                        callback: .customScheme("dinnerdecider")
                    ) { url, error in
                        if let error {
                            continuation.resume(throwing: error)
                        } else if let url {
                            continuation.resume(returning: url)
                        } else {
                            continuation.resume(throwing: URLError(.cancelled))
                        }
                    }
                    session.prefersEphemeralWebBrowserSession = false
                    session.start()
                }

                let success = await kroger.handleCallback(callbackURL)
                if success {
                    dismiss()
                } else {
                    authError = "Failed to complete sign-in. Please try again."
                }
            } catch {
                if (error as? ASWebAuthenticationSessionError)?.code == .canceledLogin {
                    // User tapped cancel in the browser — just dismiss
                    dismiss()
                } else {
                    authError = "Sign-in was interrupted. Please try again."
                }
            }
        }
    }
}
