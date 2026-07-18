import SwiftUI

/// A simple token / chip input backed by a comma-separated string so it plugs
/// straight into the existing preference keys and prompt builder.
///
/// Type a word and press return (or comma) to add a chip. Tap a chip's x to
/// remove it. Kept deliberately plain and native.
struct TokenField: View {
    @Binding var text: String
    var placeholder: String

    @State private var draft = ""
    @FocusState private var focused: Bool

    private var tokens: [String] {
        text.split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !tokens.isEmpty {
                FlowLayout(spacing: 8) {
                    ForEach(tokens, id: \.self) { token in
                        chip(token)
                    }
                }
            }
            TextField(placeholder, text: $draft)
                .focused($focused)
                .submitLabel(.done)
                .autocorrectionDisabled()
                .onSubmit(commitDraft)
                .onChange(of: draft) { _, value in
                    // Commit when the user types a comma.
                    if value.contains(",") {
                        commitDraft()
                    }
                }
        }
        .padding(.vertical, 4)
    }

    private func chip(_ token: String) -> some View {
        HStack(spacing: 4) {
            Text(token)
                .lineLimit(1)
                .foregroundStyle(Color.textPrimary)
            Button {
                remove(token)
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.body)
                    .foregroundStyle(Color.brandPrimary)
                    .frame(minWidth: 32, minHeight: 32)
                    .contentShape(Rectangle())
            }
            .accessibilityLabel("Remove \(token)")
        }
        .font(.subheadline)
        .padding(.leading, 12)
        .padding(.trailing, 4)
        .padding(.vertical, 4)
        .background(Color.brandPrimary.opacity(0.15), in: Capsule())
        .accessibilityElement(children: .combine)
        .accessibilityLabel(token)
        .accessibilityHint("Double tap to remove")
    }

    private func commitDraft() {
        let parts = draft.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
        var current = tokens
        for part in parts where !part.isEmpty {
            if !current.contains(where: { $0.caseInsensitiveCompare(part) == .orderedSame }) {
                current.append(part)
            }
        }
        text = current.joined(separator: ", ")
        draft = ""
    }

    private func remove(_ token: String) {
        text = tokens
            .filter { $0.caseInsensitiveCompare(token) != .orderedSame }
            .joined(separator: ", ")
    }
}
