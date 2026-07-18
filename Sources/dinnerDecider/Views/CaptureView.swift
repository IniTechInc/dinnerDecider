import SwiftUI
import PhotosUI

struct CaptureView: View {
    @State private var showCamera = false
    @State private var showScanning = false
    @State private var capturedImage: UIImage?
    @State private var photosPickerItem: PhotosPickerItem?

    var body: some View {
        NavigationStack {
            VStack(spacing: 32) {
                Spacer()

                Image(systemName: "camera.viewfinder")
                    .font(.system(size: 80))
                    .foregroundStyle(.tint)

                VStack(spacing: 8) {
                    Text("Scan Your Kitchen")
                        .font(.title2.bold())
                    Text("Point your camera at your fridge or pantry to identify food items.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal)
                }

                VStack(spacing: 16) {
                    Button {
                        showCamera = true
                    } label: {
                        Label("Take Photo", systemImage: "camera")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)

                    // KAN-15 (backlog/stretch): Photo library import
                    PhotosPicker(selection: $photosPickerItem, matching: .images) {
                        Label("Choose from Library", systemImage: "photo.on.rectangle")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.large)
                }
                .padding(.horizontal, 32)

                Spacer()
            }
            .navigationTitle("Scan")
            .fullScreenCover(isPresented: $showCamera) {
                CameraPickerView(image: $capturedImage)
                    .ignoresSafeArea()
            }
            .onChange(of: capturedImage) { _, image in
                if image != nil { showScanning = true }
            }
            .onChange(of: photosPickerItem) { _, item in
                Task {
                    if let data = try? await item?.loadTransferable(type: Data.self),
                       let image = UIImage(data: data) {
                        capturedImage = image
                    }
                }
            }
            .navigationDestination(isPresented: $showScanning) {
                ScanningView(image: capturedImage ?? UIImage())
            }
        }
    }
}
