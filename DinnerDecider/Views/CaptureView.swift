import PhotosUI
import SwiftUI

/// Entry screen: gather one or more photos of the fridge/pantry/shelves, then
/// scan them all in a single session.
struct CaptureView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @State private var pickedImages: [UIImage] = []
    @State private var photoItems: [PhotosPickerItem] = []
    @State private var showCamera = false
    @State private var goToScanning = false
    @State private var showCameraDeniedAlert = false
    @State private var isLoadingPicks = false

    private let locator = ModelFileLocator()

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: Spacing.xl) {
                    modelStatusBanner

                    imagePreview

                    buttons
                        .padding(.horizontal)
                }
                .padding(.vertical)
            }
            .dinnerSurfaceBackground()
            .navigationTitle("DinnerDecider")
            .sheet(isPresented: $showCamera) {
                CameraPicker { image in
                    // Downscale immediately so a full-resolution frame is never
                    // retained: keeps pageable memory low while the model holds
                    // its large wired Metal allocation during a scan.
                    let prepared = ImageDownscaler.forCapture(image)
                    withMotion(reduceMotion, .spring(response: 0.35, dampingFraction: 0.75)) {
                        pickedImages.append(prepared)
                    }
                    Haptics.tap()
                }
                .ignoresSafeArea()
            }
            .navigationDestination(isPresented: $goToScanning) {
                ScanningView(images: pickedImages)
            }
            .alert("Camera access is off", isPresented: $showCameraDeniedAlert) {
                Button("Open Settings") { PermissionHelper.openSettings() }
                Button("Not now", role: .cancel) {}
            } message: {
                Text("To take photos of your food, turn on camera access for DinnerDecider in Settings. You can also pick photos from your library instead.")
            }
            .onChange(of: photoItems) { _, newItems in
                loadPicked(newItems)
            }
        }
    }

    // MARK: - Sections

    private var imagePreview: some View {
        Group {
            if pickedImages.isEmpty {
                ContentUnavailableView(
                    "Snap your fridge or pantry",
                    systemImage: "refrigerator",
                    description: Text("Add photos of your fridge, pantry, or shelves and DinnerDecider will figure out what food you have.")
                )
                .frame(maxHeight: 300)
            } else {
                thumbnails
            }
        }
    }

    private var thumbnails: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            Text(pickedImages.count == 1 ? "1 photo ready" : "\(pickedImages.count) photos ready")
                .font(.dmSectionHeader)
                .padding(.horizontal)

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Spacing.md) {
                    ForEach(Array(pickedImages.enumerated()), id: \.offset) { index, image in
                        thumbnail(image, index: index)
                    }
                }
                .padding(.horizontal)
            }
        }
    }

    private func thumbnail(_ image: UIImage, index: Int) -> some View {
        Image(uiImage: image)
            .resizable()
            .scaledToFill()
            .frame(width: 120, height: 150)
            .clipShape(RoundedRectangle(cornerRadius: Radius.card, style: .continuous))
            .overlay(alignment: .topTrailing) {
                Button {
                    withMotion(reduceMotion) {
                        _ = pickedImages.remove(at: index)
                    }
                    Haptics.tap()
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.title3)
                        .symbolRenderingMode(.palette)
                        .foregroundStyle(.white, .black.opacity(0.5))
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Remove photo \(index + 1)")
            }
            .accessibilityLabel("Selected photo \(index + 1)")
    }

    private var buttons: some View {
        VStack(spacing: 12) {
            PhotosPicker(
                selection: $photoItems,
                maxSelectionCount: 8,
                matching: .images
            ) {
                Label(pickedImages.isEmpty ? "Choose from Photos" : "Add more from Photos", systemImage: "photo.on.rectangle")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            .controlSize(.large)

            if CameraPicker.isCameraAvailable {
                Button {
                    handleCameraTap()
                } label: {
                    Label("Take a Photo", systemImage: "camera")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
            }

            if isLoadingPicks {
                ProgressView()
                    .padding(.top, 4)
            }

            if !pickedImages.isEmpty {
                Button {
                    Haptics.tap()
                    goToScanning = true
                } label: {
                    Label(scanButtonTitle, systemImage: "sparkles")
                        .font(.dmBodyBold)
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            }
        }
    }

    private var scanButtonTitle: String {
        pickedImages.count == 1 ? "Scan this photo" : "Scan \(pickedImages.count) photos"
    }

    // MARK: - Model banner

    private var modelStatusBanner: some View {
        NavigationLink {
            ModelSetupView()
        } label: {
            HStack {
                Image(systemName: locator.isModelPresent ? "checkmark.seal.fill" : "exclamationmark.triangle.fill")
                    .foregroundStyle(locator.isModelPresent ? Color.brandSecondary : Color.brandAccent)
                VStack(alignment: .leading, spacing: 4) {
                    Text(locator.isModelPresent ? "Model ready" : "Model not installed")
                        .font(.dmSectionHeader)
                    Text(locator.isModelPresent ? "On-device AI is set up." : "Tap to set up on-device AI.")
                        .font(.dmCaption)
                        .foregroundStyle(.secondary)
                    inferenceModeBadge
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
            .padding()
            .background(.thinMaterial, in: RoundedRectangle(cornerRadius: Radius.card, style: .continuous))
            .padding(.horizontal)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityHint("Opens model setup")
    }

    /// Small badge that always shows which inference engine is actually running.
    /// Distinguished by icon and text (not colour alone) so it reads clearly for
    /// everyone and to VoiceOver.
    private var inferenceModeBadge: some View {
        HStack(spacing: Spacing.xs) {
            Image(systemName: appModel.isUsingMock ? "cpu" : "sparkles")
                .accessibilityHidden(true)
            Text(appModel.isUsingMock ? "Demo mode (sample data)" : "On-device Gemma 4")
        }
        .font(.dmCaptionMedium)
        .padding(.horizontal, Spacing.sm)
        .padding(.vertical, 3)
        .background(
            (appModel.isUsingMock ? Color.brandAccent : Color.brandSecondary).opacity(0.15),
            in: Capsule()
        )
        .foregroundStyle(appModel.isUsingMock ? Color.brandAccent : Color.brandSecondary)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(appModel.isUsingMock
            ? "Running in demo mode with sample data"
            : "Running on-device Gemma 4")
    }

    // MARK: - Actions

    private func handleCameraTap() {
        switch PermissionHelper.cameraStatus {
        case .authorized:
            showCamera = true
        case .notDetermined:
            Task {
                let granted = await PermissionHelper.requestCameraAccess()
                if granted {
                    showCamera = true
                } else {
                    showCameraDeniedAlert = true
                }
            }
        case .denied:
            showCameraDeniedAlert = true
        }
    }

    private func loadPicked(_ items: [PhotosPickerItem]) {
        guard !items.isEmpty else { return }
        isLoadingPicks = true
        Task {
            var loaded: [UIImage] = []
            for item in items {
                if let data = try? await item.loadTransferable(type: Data.self),
                   let image = UIImage(data: data) {
                    // Downscale on import so we never keep full-resolution frames
                    // resident (see ImageDownscaler for the memory rationale).
                    loaded.append(ImageDownscaler.forCapture(image))
                }
            }
            await MainActor.run {
                withMotion(reduceMotion, .spring(response: 0.35, dampingFraction: 0.75)) {
                    pickedImages.append(contentsOf: loaded)
                }
                photoItems = []
                isLoadingPicks = false
            }
        }
    }
}

#Preview {
    CaptureView()
        .environmentObject(AppModel())
}
