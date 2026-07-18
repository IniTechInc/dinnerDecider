import SwiftUI

/// KAN-24: Streaming scanning UI — items appear in real time as each crop is identified.
struct ScanningView: View {
    let image: UIImage
    @State private var detectedItems: [ScannedItem] = []
    @State private var isScanning = true
    @State private var totalCrops = 0
    @State private var processedCrops = 0
    @State private var showInventoryConfirm = false

    var body: some View {
        VStack(spacing: 0) {
            if isScanning {
                scanningProgressHeader
            }

            if detectedItems.isEmpty && !isScanning {
                // KAN-42: Zero-results empty state
                emptyResultsView
            } else {
                itemsList
            }

            if !isScanning && !detectedItems.isEmpty {
                addToInventoryButton
            }
        }
        .navigationTitle("Scanning")
        .navigationBarBackButtonHidden(isScanning)
        .task {
            await runPipeline()
        }
        .navigationDestination(isPresented: $showInventoryConfirm) {
            InventoryConfirmView(scannedItems: detectedItems)
        }
    }

    private var scanningProgressHeader: some View {
        VStack(spacing: 8) {
            ProgressView()
                .progressViewStyle(.circular)
            Text("Identifying food items…")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 12)
        .background(.bar)
    }

    private var itemsList: some View {
        List(detectedItems) { item in
            HStack {
                Image(systemName: item.category.systemImage)
                    .frame(width: 28)
                    .foregroundStyle(.tint)
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.name)
                        .font(.body)
                    if let brand = item.brand {
                        Text(brand)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer()
                confidenceBadge(item.confidence)
            }
        }
    }

    private func confidenceBadge(_ confidence: Double) -> some View {
        Text("\(Int(confidence * 100))%")
            .font(.caption2.monospacedDigit())
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(confidence > 0.7 ? Color.green.opacity(0.15) : Color.orange.opacity(0.15))
            .foregroundStyle(confidence > 0.7 ? .green : .orange)
            .clipShape(Capsule())
    }

    private var emptyResultsView: some View {
        ContentUnavailableView(
            "No Items Detected",
            systemImage: "magnifyingglass",
            description: Text("Try a closer photo with better lighting.")
        )
    }

    private var addToInventoryButton: some View {
        Button {
            showInventoryConfirm = true
        } label: {
            Text("Add \(detectedItems.count) Item\(detectedItems.count == 1 ? "" : "s") to Pantry")
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .controlSize(.large)
        .padding()
    }

    private func runPipeline() async {
        isScanning = true
        totalCrops = 0
        processedCrops = 0
        detectedItems = []

        guard ModelService.shared.isLoaded else {
            isScanning = false
            return
        }

        do {
            let items = try await ModelService.shared.identifyItems(image: image)
            withAnimation {
                detectedItems = items.filter { $0.confidence > 0.3 }
            }
        } catch {
            // Fall back to empty — user sees the empty state with retry prompt
        }

        isScanning = false
    }
}
