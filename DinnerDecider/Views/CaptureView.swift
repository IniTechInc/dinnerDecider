import PhotosUI
import SwiftUI

/// Entry screen: pick or shoot a photo of the fridge/pantry, then scan it.
struct CaptureView: View {
    @EnvironmentObject private var appModel: AppModel

    @State private var pickedImage: UIImage?
    @State private var photoItem: PhotosPickerItem?
    @State private var showCamera = false
    @State private var goToScanning = false

    private var locator = ModelFileLocator()

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 20) {
                    modelStatusBanner

                    imagePreview

                    VStack(spacing: 12) {
                        PhotosPicker(selection: $photoItem, matching: .images) {
                            Label("Choose from Photos", systemImage: "photo.on.rectangle")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.large)

                        if CameraPicker.isCameraAvailable {
                            Button {
                                showCamera = true
                            } label: {
                                Label("Take a Photo", systemImage: "camera")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(.bordered)
                            .controlSize(.large)
                        }

                        if pickedImage != nil {
                            Button {
                                goToScanning = true
                            } label: {
                                Label("Scan this photo", systemImage: "sparkles")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.large)
                            .tint(.green)
                        }
                    }
                    .padding(.horizontal)
                }
                .padding(.vertical)
            }
            .navigationTitle("DinnerDecider")
            .sheet(isPresented: $showCamera) {
                CameraPicker { image in
                    pickedImage = image
                }
                .ignoresSafeArea()
            }
            .navigationDestination(isPresented: $goToScanning) {
                if let image = pickedImage {
                    ScanningView(image: image)
                }
            }
            .onChange(of: photoItem) { _, newItem in
                Task {
                    if let data = try? await newItem?.loadTransferable(type: Data.self),
                       let image = UIImage(data: data) {
                        pickedImage = image
                    }
                }
            }
        }
    }

    private var imagePreview: some View {
        Group {
            if let image = pickedImage {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFit()
                    .frame(maxHeight: 320)
                    .clipShape(RoundedRectangle(cornerRadius: 16))
                    .padding(.horizontal)
            } else {
                ContentUnavailableView(
                    "Snap your fridge or pantry",
                    systemImage: "refrigerator",
                    description: Text("Pick a photo and DinnerDecider will figure out what food you have.")
                )
                .frame(maxHeight: 320)
            }
        }
    }

    private var modelStatusBanner: some View {
        NavigationLink {
            ModelSetupView()
        } label: {
            HStack {
                Image(systemName: locator.isModelPresent ? "checkmark.seal.fill" : "exclamationmark.triangle.fill")
                    .foregroundStyle(locator.isModelPresent ? .green : .orange)
                VStack(alignment: .leading, spacing: 2) {
                    Text(locator.isModelPresent ? "Model ready" : "Model not installed")
                        .font(.subheadline.weight(.semibold))
                    Text(locator.isModelPresent ? "On-device AI is set up." : "Tap to set up on-device AI.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .foregroundStyle(.tertiary)
            }
            .padding()
            .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
            .padding(.horizontal)
        }
        .buttonStyle(.plain)
    }
}

#Preview {
    CaptureView()
        .environmentObject(AppModel())
}
