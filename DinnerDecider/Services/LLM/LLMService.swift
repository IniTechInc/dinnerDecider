import CoreGraphics
import Foundation

/// Structured result the model returns for a single grocery item.
/// Matches the JSON schema in the implementation spec:
/// {"name": "...", "brand": "... or null", "category": "produce|...", "confidence": 0-1}
struct IdentifiedItem: Codable, Hashable, Sendable {
    let name: String
    let brand: String?
    let category: String
    let confidence: Double

    /// The category string mapped onto our typed enum (falls back to `.other`).
    var foodCategory: FoodCategory {
        FoodCategory(rawValue: category.lowercased()) ?? .other
    }
}

/// The seam the whole app talks to for on-device intelligence.
///
/// A `MockLLMService` fulfils it today so the app is fully navigable. The real
/// llama.cpp (Gemma 4 E4B + mmproj) runtime will drop in behind this same
/// protocol later without touching the UI or the pipeline.
protocol LLMService {
    /// Whether the model weights are loaded and resident in memory.
    var isLoaded: Bool { get }

    /// Load the model once and keep it resident. Safe to call again if loaded.
    func loadModel() async throws

    /// Identify a single cropped grocery image, aided by any OCR text found on it.
    func identifyItem(image: CGImage, ocrText: String) async throws -> IdentifiedItem

    /// General text generation used for recipe suggestions (text-only prompts).
    func generateText(prompt: String) async throws -> String
}

/// Errors the LLM layer can surface.
enum LLMServiceError: Error, LocalizedError {
    case modelNotLoaded
    case modelFilesMissing
    case badResponse

    var errorDescription: String? {
        switch self {
        case .modelNotLoaded:
            return "The model is not loaded yet."
        case .modelFilesMissing:
            return "The model files could not be found on this device."
        case .badResponse:
            return "The model returned a response we could not read."
        }
    }
}
