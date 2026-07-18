import Foundation
import UIKit
import LocalLLMClient
import LocalLLMClientLlama

// MARK: - KAN-7: Real LocalLLMClient integration for Gemma 4 E4B on-device inference

@MainActor
final class ModelService: ObservableObject {
    static let shared = ModelService()

    @Published var isLoaded = false

    private var client: LlamaClient?

    private init() {}

    // MARK: - KAN-11: Load model once, keep resident

    func load() async throws {
        let modelURL = modelsDirectory.appendingPathComponent("gemma-4-E4B-it-Q4_K_M.gguf")
        let mmprojURL = modelsDirectory.appendingPathComponent("mmproj-F16.gguf")

        guard FileManager.default.fileExists(atPath: modelURL.path),
              FileManager.default.fileExists(atPath: mmprojURL.path) else {
            throw ModelServiceError.modelFileMissing
        }

        let parameter = LlamaClient.Parameter(
            context: 2048,
            temperature: 0.1,
            options: .init(responseFormat: .json)
        )

        let capturedModelURL = modelURL
        let capturedMmprojURL = mmprojURL
        let capturedParameter = parameter

        let loadedClient = try await Task.detached(priority: .userInitiated) {
            try await LocalLLMClient.llama(
                url: capturedModelURL,
                mmprojURL: capturedMmprojURL,
                parameter: capturedParameter
            )
        }.value

        client = loadedClient
        isLoaded = true
    }

    // MARK: - KAN-21: Run a single per-crop identification call

    func identifyItem(image: UIImage, ocrText: String) async throws -> ScannedItem {
        guard let client = client else {
            throw ModelServiceError.notLoaded
        }

        let systemPrompt = """
        You are a food item identifier. Respond ONLY with JSON:
        {"name":"string","brand":"string or null","category":"produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other","confidence":0.0-1.0}
        """

        var userMessage = "Identify this food item."
        if !ocrText.isEmpty {
            userMessage += " Visible text: \(ocrText)."
        }

        let input = LLMInput.chat([
            .system(systemPrompt),
            .user(userMessage, attachments: [.image(image)])
        ])

        let stream = try client.textStream(from: input)
        var response = ""
        for try await chunk in stream {
            response += chunk
        }

        return try parseGemmaResponse(response)
    }

    // MARK: - KAN-17: Identify all food items in a full-scene image in a single Gemma call

    /// Identify all food items in a full-scene image in a single Gemma call.
    /// Simpler than per-crop pipeline — suitable for hackathon demo.
    func identifyItems(image: UIImage) async throws -> [ScannedItem] {
        guard let client = client else {
            throw ModelServiceError.notLoaded
        }

        let systemPrompt = """
        You are a food item identifier. Respond ONLY with a JSON array (no other text):
        [{"name":"string","brand":"string or null","category":"produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other","confidence":0.0-1.0}]
        """

        let input = LLMInput.chat([
            .system(systemPrompt),
            .user("List every food item visible in this image. Include brand names if visible.", attachments: [.image(image)])
        ])

        let stream = try client.textStream(from: input)
        var response = ""
        for try await chunk in stream {
            response += chunk
        }

        return parseItemsArray(response)
    }

    private func parseItemsArray(_ json: String) -> [ScannedItem] {
        guard let start = json.firstIndex(of: "["),
              let end = json.lastIndex(of: "]"),
              start <= end else {
            return []
        }
        let arrayString = String(json[start...end])
        guard let data = arrayString.data(using: .utf8) else { return [] }

        struct GemmaOutput: Decodable {
            let name: String
            let brand: String?
            let category: String
            let confidence: Double
        }
        do {
            let outputs = try JSONDecoder().decode([GemmaOutput].self, from: data)
            return outputs.map { output in
                let category = FoodCategory(rawValue: output.category) ?? .other
                return ScannedItem(name: output.name, brand: output.brand, category: category, confidence: output.confidence)
            }
        } catch {
            return []
        }
    }

    // MARK: - KAN-28: Generate text from a plain chat input (no image)

    /// Generate text from a plain chat input (no image). Used for recipe generation.
    func generateText(system: String, user: String) async throws -> String {
        guard let client = client else { throw ModelServiceError.notLoaded }
        let input = LLMInput.chat([.system(system), .user(user)])
        let stream = try client.textStream(from: input)
        var result = ""
        for try await chunk in stream {
            result += chunk
        }
        return result
    }

    // MARK: - KAN-40: JSON response parsing with retry/fallback

    func parseGemmaResponse(_ json: String) throws -> ScannedItem {
        let jsonString = extractJSON(from: json) ?? json
        guard let data = jsonString.data(using: .utf8) else {
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

    // MARK: - Private helpers

    private func extractJSON(from string: String) -> String? {
        guard let startIndex = string.firstIndex(of: "{"),
              let endIndex = string.lastIndex(of: "}") else {
            return nil
        }
        guard startIndex <= endIndex else { return nil }
        return String(string[startIndex...endIndex])
    }
}

// MARK: - ModelServiceError

enum ModelServiceError: LocalizedError {
    case notLoaded
    case inferenceTimeout
    case invalidResponse(String)
    case modelFileMissing

    var errorDescription: String? {
        switch self {
        case .notLoaded: return "Model is not loaded."
        case .inferenceTimeout: return "Inference timed out."
        case .invalidResponse(let s): return "Invalid model response: \(s)"
        case .modelFileMissing: return "Model files not found in Documents/Models/."
        }
    }
}
