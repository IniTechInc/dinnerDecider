import SwiftData
import SwiftUI

/// Runs the scan pipeline and streams identified items in. When finished the
/// user confirms them into the inventory.
struct ScanningView: View {
    let image: UIImage

    @EnvironmentObject private var appModel: AppModel
    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss

    @State private var didStart = false

    var body: some View {
        VStack(spacing: 0) {
            header

            List {
                if appModel.scannedItems.isEmpty && !isFinished {
                    Section {
                        HStack(spacing: 12) {
                            ProgressView()
                            Text("Looking for items...")
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                if !appModel.scannedItems.isEmpty {
                    Section("Found so far") {
                        ForEach(appModel.scannedItems, id: \.self) { item in
                            IdentifiedRow(item: item)
                        }
                    }
                }
            }
            .listStyle(.insetGrouped)

            if isFinished {
                confirmBar
            }
        }
        .navigationTitle("Scanning")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            guard !didStart else { return }
            didStart = true
            await appModel.scan(image: image)
        }
    }

    private var isFinished: Bool {
        if case .finished = appModel.scanPhase { return true }
        return false
    }

    private var header: some View {
        VStack(spacing: 8) {
            Image(uiImage: image)
                .resizable()
                .scaledToFill()
                .frame(height: 120)
                .clipped()
            Text(statusText)
                .font(.subheadline)
                .foregroundStyle(.secondary)
        }
        .padding(.bottom, 8)
    }

    private var statusText: String {
        switch appModel.scanPhase {
        case .idle: return "Getting ready..."
        case .cropping: return "Splitting the photo into items..."
        case let .identifying(done, total): return "Identifying item \(done + 1) of \(total)..."
        case .finished: return "Review the items, then add them to your inventory."
        }
    }

    private var confirmBar: some View {
        VStack(spacing: 12) {
            Button {
                addAllToInventory()
            } label: {
                Label("Add \(appModel.scannedItems.count) items to inventory", systemImage: "tray.and.arrow.down.fill")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(appModel.scannedItems.isEmpty)

            Button("Cancel", role: .cancel) {
                appModel.resetScan()
                dismiss()
            }
        }
        .padding()
        .background(.thinMaterial)
    }

    private func addAllToInventory() {
        let photoID = UUID().uuidString
        for identified in appModel.scannedItems {
            let item = InventoryItem(
                name: identified.name,
                brand: identified.brand,
                category: identified.foodCategory,
                sourcePhotoID: photoID
            )
            modelContext.insert(item)
        }
        appModel.resetScan()
        dismiss()
    }
}

private struct IdentifiedRow: View {
    let item: IdentifiedItem

    var body: some View {
        HStack {
            Image(systemName: item.foodCategory.symbolName)
                .foregroundStyle(.tint)
                .frame(width: 28)
            VStack(alignment: .leading, spacing: 2) {
                Text(item.name)
                    .font(.body.weight(.medium))
                if let brand = item.brand, !brand.isEmpty {
                    Text(brand)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text("\(Int(item.confidence * 100))%")
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
        }
    }
}
