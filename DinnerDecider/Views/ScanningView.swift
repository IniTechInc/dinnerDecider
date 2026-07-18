import SwiftData
import SwiftUI

/// An editable row in the review list. Seeded from what the model identified so
/// the user can rename, recategorise or delete before anything is saved.
struct ReviewItem: Identifiable, Hashable {
    let id = UUID()
    var name: String
    var brand: String?
    var category: FoodCategory
    var confidence: Double

    /// Below this the identification is uncertain and gets flagged for a look.
    var needsReview: Bool { confidence < 0.5 }

    init(from identified: IdentifiedItem) {
        self.name = identified.name
        self.brand = identified.brand
        self.category = identified.foodCategory
        self.confidence = identified.confidence
    }
}

/// Runs the scan pipeline over the batch and streams identified items in. When
/// finished the user reviews, edits, and confirms them into the inventory.
struct ScanningView: View {
    let images: [UIImage]

    @EnvironmentObject private var appModel: AppModel
    @Environment(\.modelContext) private var modelContext
    @Environment(\.dismiss) private var dismiss
    @Query private var inventory: [InventoryItem]

    @State private var didStart = false
    @State private var reviewItems: [ReviewItem] = []
    @State private var editing: ReviewItem?

    var body: some View {
        VStack(spacing: 0) {
            header

            content

            footer
        }
        .navigationTitle("Scan")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if isScanning {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Cancel") { appModel.cancelScan() }
                }
            }
        }
        .sheet(item: $editing) { item in
            ReviewEditorView(item: item) { updated in
                if let index = reviewItems.firstIndex(where: { $0.id == updated.id }) {
                    reviewItems[index] = updated
                }
            }
        }
        .onChange(of: appModel.scanPhase) { _, phase in
            if phase == .finished {
                seedReviewItems()
            }
        }
        .onAppear {
            guard !didStart else { return }
            didStart = true
            appModel.startScan(images: images)
        }
    }

    // MARK: - State helpers

    private var isScanning: Bool {
        if case .scanning = appModel.scanPhase { return true }
        return false
    }

    private var isFinished: Bool { appModel.scanPhase == .finished }
    private var isEmpty: Bool { appModel.scanPhase == .empty }
    private var isCancelled: Bool { appModel.scanPhase == .cancelled }

    private func seedReviewItems() {
        reviewItems = appModel.scannedItems.map(ReviewItem.init(from:))
    }

    // MARK: - Header

    private var header: some View {
        VStack(spacing: 8) {
            if let first = images.first {
                Image(uiImage: first)
                    .resizable()
                    .scaledToFill()
                    .frame(height: 120)
                    .clipped()
                    .overlay(alignment: .bottomLeading) {
                        if images.count > 1 {
                            Text("\(images.count) photos")
                                .font(.caption2.weight(.semibold))
                                .padding(.horizontal, 8)
                                .padding(.vertical, 4)
                                .background(.ultraThinMaterial, in: Capsule())
                                .padding(8)
                        }
                    }
            }
            Text(statusText)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal)
        }
        .padding(.bottom, 8)
    }

    private var statusText: String {
        switch appModel.scanPhase {
        case .idle:
            return "Getting ready..."
        case let .scanning(progress):
            let photoPart = progress.photoCount > 1
                ? "Photo \(progress.photoIndex + 1) of \(progress.photoCount): "
                : ""
            switch progress.stage {
            case .cropping:
                return "\(photoPart)splitting the photo into items..."
            case .identifying:
                let total = max(progress.itemsTotal, 1)
                return "\(photoPart)identifying item \(min(progress.itemsDone + 1, total)) of \(total)..."
            }
        case .finished:
            return "Review the items, then add them to your inventory."
        case .empty:
            return "We could not spot anything to add."
        case .cancelled:
            return "Scan cancelled."
        }
    }

    // MARK: - Content

    @ViewBuilder
    private var content: some View {
        if isEmpty {
            emptyResults
        } else if isCancelled {
            cancelledState
        } else if isFinished {
            reviewList
        } else {
            scanningList
        }
    }

    private var scanningList: some View {
        List {
            Section {
                ForEach(appModel.scannedItems, id: \.self) { item in
                    IdentifiedRow(item: item)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
                HStack(spacing: 12) {
                    ProgressView()
                    Text(appModel.scannedItems.isEmpty ? "Looking for items..." : "Still looking...")
                        .foregroundStyle(.secondary)
                }
            } header: {
                if !appModel.scannedItems.isEmpty {
                    Text("Found so far")
                }
            }
        }
        .listStyle(.insetGrouped)
        .dinnerSurfaceBackground()
    }

    private var reviewList: some View {
        List {
            if reviewItems.contains(where: { $0.needsReview }) {
                Section {
                    Label("Tap any item to fix its name or category before saving.", systemImage: "hand.tap")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            Section("Found \(reviewItems.count) \(reviewItems.count == 1 ? "item" : "items")") {
                ForEach(reviewItems) { item in
                    Button {
                        editing = item
                    } label: {
                        ReviewRow(item: item)
                    }
                    .buttonStyle(.plain)
                }
                .onDelete { offsets in
                    reviewItems.remove(atOffsets: offsets)
                }
            }
        }
        .listStyle(.insetGrouped)
        .dinnerSurfaceBackground()
    }

    private var emptyResults: some View {
        ContentUnavailableView {
            Label("Nothing spotted", systemImage: "eye.slash")
        } description: {
            Text("We could not spot anything. Try getting closer, adding more light, or filling more of the frame.")
        } actions: {
            Button {
                dismiss()
            } label: {
                Label("Try another photo", systemImage: "camera")
            }
            .buttonStyle(.borderedProminent)
        }
    }

    private var cancelledState: some View {
        ContentUnavailableView {
            Label("Scan cancelled", systemImage: "xmark.circle")
        } description: {
            Text("No items were saved. You can start again whenever you like.")
        } actions: {
            if !appModel.scannedItems.isEmpty {
                Button {
                    seedReviewItems()
                    appModel.scanPhase = .finished
                } label: {
                    Label("Keep the \(appModel.scannedItems.count) found", systemImage: "tray.and.arrow.down")
                }
                .buttonStyle(.borderedProminent)
            }
            Button("Back") { dismiss() }
        }
    }

    // MARK: - Footer

    @ViewBuilder
    private var footer: some View {
        if isFinished {
            VStack(spacing: 8) {
                Button {
                    addAllToInventory()
                } label: {
                    Label(
                        "Add \(reviewItems.count) \(reviewItems.count == 1 ? "item" : "items") to inventory",
                        systemImage: "tray.and.arrow.down.fill"
                    )
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(reviewItems.isEmpty)

                Button("Discard", role: .cancel) {
                    appModel.resetScan()
                    dismiss()
                }
            }
            .padding()
            .background(.thinMaterial)
        }
    }

    // MARK: - Confirm

    private func addAllToInventory() {
        let photoID = UUID().uuidString
        let existing = inventory.map { InventoryLogic.StockRef(key: $0.mergeKey, quantity: $0.quantity) }
        let confirming = reviewItems.map {
            (key: InventoryLogic.mergeKey(name: $0.name, brand: $0.brand), quantity: 1)
        }
        let actions = InventoryLogic.confirmPlan(confirming: confirming, existing: existing)

        for (index, action) in actions.enumerated() {
            switch action {
            case let .increment(key, newQuantity):
                if let match = inventory.first(where: { $0.mergeKey == key }) {
                    match.quantity = newQuantity
                }
            case let .insert(_, quantity):
                let review = reviewItems[index]
                let trimmed = review.name.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !trimmed.isEmpty else { continue }
                let item = InventoryItem(
                    name: trimmed,
                    brand: review.brand?.isEmpty == true ? nil : review.brand,
                    category: review.category,
                    quantity: quantity,
                    sourcePhotoID: photoID
                )
                modelContext.insert(item)
            }
        }
        Haptics.success()
        appModel.resetScan()
        dismiss()
    }
}

// MARK: - Rows

/// A read-only row shown while items stream in.
private struct IdentifiedRow: View {
    let item: IdentifiedItem

    var body: some View {
        HStack {
            Image(systemName: item.foodCategory.symbolName)
                .foregroundStyle(.tint)
                .frame(width: 28)
                .accessibilityHidden(true)
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
            ConfidenceBadge(confidence: item.confidence)
        }
        .accessibilityElement(children: .combine)
    }
}

/// An editable review row with a low-confidence flag.
private struct ReviewRow: View {
    let item: ReviewItem

    var body: some View {
        HStack {
            Image(systemName: item.category.symbolName)
                .foregroundStyle(.tint)
                .frame(width: 28)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(item.name)
                    .font(.body.weight(.medium))
                    .foregroundStyle(.primary)
                if let brand = item.brand, !brand.isEmpty {
                    Text(brand)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            if item.needsReview {
                Label("Check", systemImage: "questionmark.circle.fill")
                    .labelStyle(.iconOnly)
                    .foregroundStyle(Color.brandAccent)
                    .accessibilityLabel("Low confidence, please check")
            }
            ConfidenceBadge(confidence: item.confidence)
            Image(systemName: "chevron.right")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityHint("Tap to edit")
    }
}

/// A little confidence chip, colour coded.
struct ConfidenceBadge: View {
    let confidence: Double

    private var percent: Int { Int((confidence * 100).rounded()) }
    // Sage reads as "confident"; amber as "worth a glance". Below 0.5 the row
    // also shows an amber Check flag, keeping caution a single colour.
    private var color: Color {
        confidence >= 0.75 ? .brandSecondary : .brandAccent
    }

    var body: some View {
        Text("\(percent)%")
            .font(.dmCaptionMedium)
            .monospacedDigit()
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(color.opacity(0.15), in: Capsule())
            .accessibilityLabel("\(percent) percent confident")
    }
}

// MARK: - Review editor

private struct ReviewEditorView: View {
    let item: ReviewItem
    var onSave: (ReviewItem) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var brand: String
    @State private var category: FoodCategory

    init(item: ReviewItem, onSave: @escaping (ReviewItem) -> Void) {
        self.item = item
        self.onSave = onSave
        _name = State(initialValue: item.name)
        _brand = State(initialValue: item.brand ?? "")
        _category = State(initialValue: item.category)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Item") {
                    TextField("Name", text: $name)
                    TextField("Brand (optional)", text: $brand)
                }
                Section("Category") {
                    Picker("Category", selection: $category) {
                        ForEach(FoodCategory.allCases) { option in
                            Label(option.displayName, systemImage: option.symbolName).tag(option)
                        }
                    }
                    .pickerStyle(.navigationLink)
                }
            }
            .navigationTitle("Edit item")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        var updated = item
                        updated.name = name.trimmingCharacters(in: .whitespacesAndNewlines)
                        updated.brand = brand.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : brand
                        updated.category = category
                        onSave(updated)
                        dismiss()
                    }
                    .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
    }
}
