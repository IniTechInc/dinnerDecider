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
            Button {
                remove(token)
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.caption)
            }
            .accessibilityLabel("Remove \(token)")
        }
        .font(.subheadline)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(Color.accentColor.opacity(0.15), in: Capsule())
        .foregroundStyle(Color.accentColor)
        .accessibilityElement(children: .combine)
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

/// Minimal flow layout so chips wrap onto multiple lines.
struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Void) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var rows: [[CGSize]] = [[]]
        var currentWidth: CGFloat = 0
        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if currentWidth + size.width > maxWidth, !(rows.last?.isEmpty ?? true) {
                rows.append([])
                currentWidth = 0
            }
            rows[rows.count - 1].append(size)
            currentWidth += size.width + spacing
        }
        let height = rows.reduce(CGFloat(0)) { partial, row in
            partial + (row.map(\.height).max() ?? 0) + spacing
        } - spacing
        return CGSize(width: maxWidth == .infinity ? currentWidth : maxWidth, height: max(height, 0))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Void) {
        let maxWidth = bounds.width
        var x = bounds.minX
        var y = bounds.minY
        var rowHeight: CGFloat = 0
        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > bounds.minX + maxWidth, x > bounds.minX {
                x = bounds.minX
                y += rowHeight + spacing
                rowHeight = 0
            }
            subview.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}
