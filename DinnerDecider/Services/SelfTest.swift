import Foundation
import UIKit

/// Headless self-test for the on-device Gemma runtime.
///
/// Launched with the `--llm-selftest` argument (see `DinnerDeciderApp`), this
/// loads the model, runs a single image identification on a small bundled test
/// image, records peak memory, writes the result to
/// `Documents/selftest_result.txt`, and then idles. It lets the build-and-ship
/// orchestrator reproduce the memory behaviour of a real scan on the phone
/// without driving the UI:
///
///   xcrun devicectl device process launch \
///     --device <DEVICE-ID> --start-stopped=false --terminate-existing \
///     com.philwoolley.dinnerdecider --llm-selftest
///   # wait ~60s, then pull the result:
///   xcrun devicectl device copy from \
///     --device <DEVICE-ID> \
///     --domain-type appDataContainer \
///     --domain-identifier com.philwoolley.dinnerdecider \
///     --source Documents/selftest_result.txt \
///     --destination ./selftest_result.txt
///
/// The peak footprint reported here is the app's *own* footprint. The crash was a
/// systemwide `vm-pageshortage` driven by WIRED Metal memory (the weights the GPU
/// holds), which does not show up in this per-process number, so a healthy
/// footprint here plus a successful load/identify is the signal that the model
/// fits and runs, not proof of the systemwide wired total.
enum SelfTest {

    /// Launch argument that triggers the self-test instead of the normal UI.
    static let launchArgument = "--llm-selftest"

    /// Whether this process was launched to run the self-test.
    static var isRequested: Bool {
        CommandLine.arguments.contains(launchArgument)
    }

    /// Basename of the bundled test image (added as a bundle resource).
    static let testImageName = "selftest"

    /// Output filename written into the app's Documents directory.
    static let resultFileName = "selftest_result.txt"

    /// Run the self-test end to end and return the human-readable result line
    /// (also written to `Documents/selftest_result.txt` and logged via NSLog).
    @discardableResult
    static func run() async -> String {
        let start = Date()
        let footprintStart = MemoryProbe.footprintBytes()
        var peakFootprint = footprintStart

        func sample() {
            peakFootprint = max(peakFootprint, MemoryProbe.footprintBytes())
        }

        let locator = ModelFileLocator()
        guard locator.isModelPresent,
              let modelName = locator.modelURL?.lastPathComponent,
              let mmprojName = locator.mmprojURL?.lastPathComponent else {
            return finish(
                status: "MODEL_MISSING",
                detail: "No GGUF weights + mmproj found in Documents or the app bundle.",
                model: "-",
                mmproj: "-",
                peakFootprint: peakFootprint,
                elapsed: Date().timeIntervalSince(start)
            )
        }

        guard let image = loadTestImage(), let cgImage = image.cgImage else {
            return finish(
                status: "TEST_IMAGE_MISSING",
                detail: "Could not load bundled '\(testImageName)' image.",
                model: modelName,
                mmproj: mmprojName,
                peakFootprint: peakFootprint,
                elapsed: Date().timeIntervalSince(start)
            )
        }

        let service = GemmaLLMService(locator: locator)
        do {
            try await service.loadModel()
            sample()
        } catch {
            return finish(
                status: "LOAD_FAILED",
                detail: "\(error)",
                model: modelName,
                mmproj: mmprojName,
                peakFootprint: peakFootprint,
                elapsed: Date().timeIntervalSince(start)
            )
        }

        do {
            let item = try await service.identifyItem(image: cgImage, ocrText: "OATMEAL Original Flavor")
            sample()
            let detail = "name=\(item.name) brand=\(item.brand ?? "null") "
                + "category=\(item.category) confidence=\(item.confidence)"
            return finish(
                status: "OK",
                detail: detail,
                model: modelName,
                mmproj: mmprojName,
                peakFootprint: peakFootprint,
                elapsed: Date().timeIntervalSince(start)
            )
        } catch {
            sample()
            return finish(
                status: "IDENTIFY_FAILED",
                detail: "\(error)",
                model: modelName,
                mmproj: mmprojName,
                peakFootprint: peakFootprint,
                elapsed: Date().timeIntervalSince(start)
            )
        }
    }

    // MARK: - Helpers

    private static func loadTestImage() -> UIImage? {
        if let url = Bundle.main.url(forResource: testImageName, withExtension: "png"),
           let data = try? Data(contentsOf: url),
           let image = UIImage(data: data) {
            return image
        }
        return UIImage(named: testImageName)
    }

    private static func finish(
        status: String,
        detail: String,
        model: String,
        mmproj: String,
        peakFootprint: UInt64,
        elapsed: TimeInterval
    ) -> String {
        let available = MemoryProbe.availableBytes()
        let iso = ISO8601DateFormatter().string(from: Date())
        let peakMB = Double(peakFootprint) / 1_048_576
        let availMB = available.map { Double($0) / 1_048_576 }
        let availText = availMB.map { String(format: "%.0fMB", $0) } ?? "n/a"
        let line = String(
            format: "[%@] status=%@ model=%@ mmproj=%@ peakFootprint=%.0fMB available=%@ elapsed=%.1fs detail=%@",
            iso, status, model, mmproj, peakMB, availText, elapsed, detail
        )
        NSLog("SELFTEST %@", line)
        writeResult(line)
        return line
    }

    private static func writeResult(_ line: String) {
        guard let docs = try? FileManager.default.url(
            for: .documentDirectory, in: .userDomainMask, appropriateFor: nil, create: true
        ) else { return }
        let url = docs.appendingPathComponent(resultFileName)
        try? (line + "\n").data(using: .utf8)?.write(to: url, options: .atomic)
    }
}

/// Small wrapper around the Mach/Darwin memory APIs used by the self-test.
enum MemoryProbe {

    /// The app's current physical memory footprint in bytes (the number iOS uses
    /// for footprint-based jetsam), or 0 if it cannot be read.
    static func footprintBytes() -> UInt64 {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(
            MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size
        )
        let result = withUnsafeMutablePointer(to: &info) { pointer in
            pointer.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { rebound in
                task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), rebound, &count)
            }
        }
        return result == KERN_SUCCESS ? info.phys_footprint : 0
    }

    /// Bytes the app can still allocate before iOS is likely to jetsam it for its
    /// own footprint, or nil where the API is unavailable.
    static func availableBytes() -> UInt64? {
        #if os(iOS)
        let available = os_proc_available_memory()
        return available > 0 ? UInt64(available) : nil
        #else
        return nil
        #endif
    }
}
