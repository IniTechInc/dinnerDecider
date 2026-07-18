import AVFoundation
import UIKit

/// Thin helper around camera authorization so the Capture screen can show a
/// friendly explanation and an Open Settings button when access is denied.
///
/// Note: the photo library uses `PhotosPicker` (PHPicker), which runs out of
/// process and needs no photo-library permission at all, so there is nothing to
/// gate there. Camera access is the only real permission the app requests.
enum PermissionHelper {

    enum CameraStatus {
        case authorized
        case notDetermined
        case denied
    }

    static var cameraStatus: CameraStatus {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            return .authorized
        case .notDetermined:
            return .notDetermined
        case .denied, .restricted:
            return .denied
        @unknown default:
            return .denied
        }
    }

    /// Ask for camera access. Returns true if granted.
    static func requestCameraAccess() async -> Bool {
        await AVCaptureDevice.requestAccess(for: .video)
    }

    /// Open the app's page in Settings so the user can flip the toggle.
    @MainActor
    static func openSettings() {
        guard let url = URL(string: UIApplication.openSettingsURLString) else { return }
        UIApplication.shared.open(url)
    }
}
