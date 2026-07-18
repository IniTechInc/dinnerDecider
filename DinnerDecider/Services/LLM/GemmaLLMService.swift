import CoreGraphics
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
                // Context is deliberately 1536, not the 2048 default. On an 8GB
                // iPhone the whole model runs GPU-resident (llama.cpp uploads the
                // weights, mmproj and KV cache into Metal buffers, which count as
                // WIRED kernel memory). A vm-pageshortage jetsam killed us at
                // ~5.1GB systemwide wired even though our own footprint was only
                // ~400MB. Every token of context is KV cache that lives in that
                // wired budget; our prompts and replies are short (a single item
                // JSON, or a compact recipe list), so 1536 is comfortably enough
                // and trims the KV allocation by ~25% vs 2048. See the memory
                // notes in the crash write-up before raising this again.
                context: 1536,
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
            output += token
        }
        return output
    }

    /// Downscale so the longest side is <= `maxImageSide`, opaque, scale 1.
    private static func downscaled(_ cgImage: CGImage) -> UIImage {
        let width = CGFloat(cgImage.width)
        let height = CGFloat(cgImage.height)
        let longest = max(width, height)
        let source = UIImage(cgImage: cgImage)
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
