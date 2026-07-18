import CoreGraphics
import CoreImage
import Foundation
import UIKit
import LocalLLMClient
import LocalLLMClientLlama

/// The real on-device intelligence: Gemma 4 E4B (Q4_0) + vision projector,
/// running entirely on the phone via llama.cpp behind the LocalLLMClient wrapper.
///
/// Design notes (verified in the macOS harness against this exact GGUF):
/// - One resident, grammar-free `LlamaClient` serves both `identifyItem` and
///   `generateText`. LocalLLMClient 0.5.0 does expose `responseFormat` grammar
///   and it applies from token zero (killing Gemma's thinking preamble), but its
///   grammar sampler is never reset between calls, so it produces output only on
///   the FIRST call and then goes terminal. That makes it unusable for a
///   multi-crop scan. The grammar-free path returns clean JSON here anyway, and
///   `LLMResponseParser` strips any stray Gemma `<|channel>thought ... <channel|>`
///   preamble as a safety net for messier real-world photos.
/// - Images are downscaled so the longest side is <= 896px before inference to
///   stay within the on-device memory budget.
/// - Load once, keep resident (the model file is ~4.5GB).
final class GemmaLLMService: LLMService {

    private let locator: ModelFileLocator
    private var client: LlamaClient?
    private(set) var isLoaded = false

    /// Longest-side pixel cap for images sent to the vision tower.
    private static let maxImageSide: CGFloat = 896

    init(locator: ModelFileLocator = ModelFileLocator()) {
        self.locator = locator
    }

    // MARK: - Loading

    func loadModel() async throws {
        if client != nil {
            isLoaded = true
            return
        }
        guard let modelURL = locator.modelURL, let mmprojURL = locator.mmprojURL else {
            throw LLMServiceError.modelFilesMissing
        }
        client = try await LocalLLMClient.llama(
            url: modelURL,
            mmprojURL: mmprojURL,
            parameter: .init(
                // Context is deliberately 1024, not the 2048 default. On an 8GB
                // iPhone the whole model runs GPU-resident (llama.cpp uploads the
                // weights, mmproj and KV cache into Metal buffers, which count as
                // WIRED kernel memory). A vm-pageshortage jetsam killed us at
                // ~5.1GB systemwide wired even though our own footprint was only
                // ~400MB. Every token of context is KV cache that lives in that
                // wired budget; our prompts and replies are short (a single item
                // JSON with ~256 image tokens, or a compact recipe list), so 1024
                // is still comfortably enough. Was 1536; dropped further after a
                // field crash at scan start on the IQ3_XXS build (escalation
                // lever 1 in the memory/OOM notes). See those notes before
                // raising this again.
                context: 1024,
                // MUST stay >= one image's vision-token chunk. parameter.batch
                // is passed straight to the mtmd image evaluator and sizes
                // llama_batch_init; at 256 (build 5) every image chunk failed to
                // evaluate and scans found nothing. 512 is the proven value.
                batch: 512,
                temperature: 0.2,
                topK: 40,
                topP: 0.95
            )
        )
        isLoaded = true
    }

    /// Drop the resident client so llama.cpp frees its weights, mmproj and KV
    /// cache (several GB of WIRED Metal memory). Called on memory pressure to
    /// avoid a systemwide `vm-pageshortage` jetsam; the next scan reloads it. An
    /// in-flight `textStream` keeps its own strong reference to the client it was
    /// started with, so nil-ing the property here cannot tear a running stream out
    /// from under itself.
    func unloadModel() {
        client = nil
        isLoaded = false
    }

    // MARK: - Inference

    func identifyItem(image: CGImage, ocrText: String) async throws -> IdentifiedItem {
        guard client != nil else { throw LLMServiceError.modelNotLoaded }
        let uiImage = Self.downscaled(image)
        let trimmed = ocrText.trimmingCharacters(in: .whitespacesAndNewlines)
        let ocrLine = trimmed.isEmpty ? "none" : trimmed
        let prompt = """
        You identify a single grocery item from the photo. \
        Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. \
        If no grocery item is clearly visible, use name "unknown" with confidence 0. \
        Text found on the packaging: \(ocrLine). \
        Respond ONLY with JSON, no other words: \
        {"name": "...", "brand": "... or null", \
        "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", \
        "confidence": 0-1}
        """
        let text = try await complete(prompt: prompt, attachments: [.image(uiImage)])
        guard let item = LLMResponseParser.decode(IdentifiedItem.self, from: text) else {
            throw LLMServiceError.badResponse
        }
        return item
    }

    func identifyAllItems(image: CGImage, ocrText: String) async throws -> [IdentifiedItem] {
        guard client != nil else { throw LLMServiceError.modelNotLoaded }
        let uiImage = Self.downscaled(image)
        let trimmed = ocrText.trimmingCharacters(in: .whitespacesAndNewlines)
        let ocrLine = trimmed.isEmpty ? "none" : trimmed
        let prompt = """
        List ALL food and grocery items visible in this photo. \
        Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. \
        Text found in the image: \(ocrLine). \
        Respond ONLY with a JSON array, no other words: \
        [{"name": "...", "brand": "... or null", \
        "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", \
        "confidence": 0-1}]
        """
        let text = try await complete(prompt: prompt, attachments: [.image(uiImage)])
        return LLMResponseParser.decodeArray(IdentifiedItem.self, from: text) ?? []
    }

    func generateText(prompt: String) async throws -> String {
        guard client != nil else { throw LLMServiceError.modelNotLoaded }
        return try await complete(prompt: prompt, attachments: [])
    }

    // MARK: - Helpers

    private func complete(prompt: String, attachments: [LLMAttachment]) async throws -> String {
        guard let client else { throw LLMServiceError.modelNotLoaded }
        let input = LLMInput.chat([.user(prompt, attachments: attachments)])
        var output = ""
        for try await token in try client.textStream(from: input) {
            if Task.isCancelled { break }
            output += token
        }
        return output
    }

    /// Reusable CIContext for image preprocessing. Creating one per-crop would
    /// allocate a new Metal GPU context each time, exhausting wired memory on
    /// top of the already ~3.5GB GPU-resident model and triggering jetsam.
    /// Software renderer on purpose: the tone-curve pass on a <=896px crop is
    /// milliseconds on CPU, and it keeps preprocessing entirely out of the
    /// wired GPU budget the model is already straining.
    private static let ciContext = CIContext(options: [.useSoftwareRenderer: true])

    /// Downscale so the longest side is <= `maxImageSide`, normalize exposure, opaque, scale 1.
    private static func downscaled(_ cgImage: CGImage) -> UIImage {
        let width = CGFloat(cgImage.width)
        let height = CGFloat(cgImage.height)
        let longest = max(width, height)

        // Auto-adjust exposure and contrast for dim fridge/pantry photos.
        let normalized: CGImage = {
            let ciImage = CIImage(cgImage: cgImage)
            let adjusted = ciImage.applyingFilter("CIToneCurve", parameters: [
                "inputPoint0": CIVector(x: 0.0, y: 0.05),
                "inputPoint1": CIVector(x: 0.25, y: 0.28),
                "inputPoint2": CIVector(x: 0.5, y: 0.55),
                "inputPoint3": CIVector(x: 0.75, y: 0.78),
                "inputPoint4": CIVector(x: 1.0, y: 1.0)
            ])
            return ciContext.createCGImage(adjusted, from: adjusted.extent) ?? cgImage
        }()

        let source = UIImage(cgImage: normalized)
        guard longest > maxImageSide else { return source }

        let scale = maxImageSide / longest
        let target = CGSize(width: (width * scale).rounded(), height: (height * scale).rounded())
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        format.opaque = true
        let renderer = UIGraphicsImageRenderer(size: target, format: format)
        return renderer.image { _ in
            source.draw(in: CGRect(origin: .zero, size: target))
        }
    }
}
