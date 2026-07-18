import Foundation

/// Placeholder for KAN-7/KAN-8: llama.cpp integration via LocalLLMClient.
/// Replace this stub with real LocalLLMClient calls once the package dependency is resolved.
@MainActor
final class ModelService: ObservableObject {
    static let shared = ModelService()

    @Published var isLoaded = false

    private init() {}

    /// KAN-11: Load model once, keep resident.
    func load() async throws {
        // TODO: KAN-7 — wire LocalLLMClient here
        // let config = LLMConfiguration(modelPath: ggufURL.path, mmprojPath: mmprojURL.path)
        // model = try await LLMModel(configuration: config)
        try await Task.sleep(for: .seconds(2)) // simulate load time
        isLoaded = true
    }

    /// KAN-21: Run a single per-crop identification call.
    /// - Parameters:
    ///   - image: The cropped UIImage for this item.
    ///   - ocrText: Text extracted by VNRecognizeTextRequest from this crop.
    /// - Returns: A ScannedItem parsed from Gemma's JSON response.
    func identifyItem(image: Any, ocrText: String) async throws -> ScannedItem {
        // TODO: KAN-21 — call LocalLLMClient with image + prompt
        // let prompt = "You identify grocery items. Text found on the packaging: \(ocrText). Respond ONLY with JSON: ..."
        // let response = try await model.generate(prompt: prompt, image: image)
        // return try parseGemmaResponse(response)
        throw ModelServiceError.notLoaded
    }

    // MARK: - KAN-40: JSON response parsing with retry/fallback

    func parseGemmaResponse(_ json: String) throws -> ScannedItem {
        guard let data = json.data(using: .utf8) else {
            return .unknown()
        }
        struct GemmaOutput: Decodable {
            let name: String
            let brand: String?
            let category: String
            let confidence: Double
        }
        do {
            let output = try JSONDecoder().decode(GemmaOutput.self, from: data)
            let category = FoodCategory(rawValue: output.category) ?? .other
            return ScannedItem(name: output.name, brand: output.brand, category: category, confidence: output.confidence)
        } catch {
            return .unknown()
        }
    }
}

enum ModelServiceError: LocalizedError {
    case notLoaded
    case inferenceTimeout
    case invalidResponse(String)

    var errorDescription: String? {
        switch self {
        case .notLoaded: return "Model is not loaded."
        case .inferenceTimeout: return "Inference timed out."
        case .invalidResponse(let s): return "Invalid model response: \(s)"
        }
    }
}
