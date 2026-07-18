import CoreGraphics
import Vision

/// Reads packaging text off a crop so it can be fused into the model prompt.
///
/// Per the spec, image + OCR text beats image-only for fine-grained packaged
/// grocery recognition (the "look-alike products" failure mode). Runs on-device
/// in accurate mode.
enum OCRService {

    static func recognizeText(in cgImage: CGImage) -> String {
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.usesLanguageCorrection = true

        let handler = VNImageRequestHandler(cgImage: cgImage, options: [:])
        try? handler.perform([request])

        let lines = (request.results ?? []).compactMap {
            $0.topCandidates(1).first?.string
        }
        return lines.joined(separator: " ")
    }
}
