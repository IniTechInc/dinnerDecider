import Foundation

/// Pure, unit-testable helpers for pulling structured JSON out of a model's reply.
///
/// Language models love to wrap their JSON in prose ("Sure, here you go:") and
/// markdown fences (```json ... ```). These functions strip that noise and hand
/// back the first balanced JSON object so we can decode it.
enum LLMResponseParser {

    /// Remove markdown code fences so they do not confuse the scanner.
    static func stripCodeFences(_ text: String) -> String {
        var result = text
        for fence in ["```json", "```JSON", "```Json", "```"] {
            result = result.replacingOccurrences(of: fence, with: "")
        }
        return result
    }

    /// Strip Gemma 4's "thinking" channel preamble.
    ///
    /// The July 2026 Gemma 4 chat template wraps chain-of-thought as
    /// `<|channel>thought\n ...reasoning... \n<channel|>` immediately before the
    /// real answer. On messy real-world photos the model sometimes emits this
    /// even with thinking nominally off, and the reasoning text can contain
    /// brace characters that would fool the JSON extractor. Keeping only the
    /// text after the LAST `<channel|>` close marker discards the reasoning and
    /// leaves the final answer. If no marker is present the text is unchanged.
    static func stripReasoning(_ text: String) -> String {
        if let range = text.range(of: "<channel|>", options: .backwards) {
            return String(text[range.upperBound...])
        }
        return text
    }

    /// Return the first balanced `{ ... }` JSON object found in `text`, or nil.
    ///
    /// Braces that appear inside string literals (and escaped quotes) are ignored,
    /// so values like `{"note": "a}b"}` parse correctly.
    static func firstJSONObject(in text: String) -> String? {
        let cleaned = stripCodeFences(stripReasoning(text))
        let chars = Array(cleaned)
        guard let start = chars.firstIndex(of: "{") else { return nil }

        var depth = 0
        var inString = false
        var escaped = false
        var index = start

        while index < chars.count {
            let character = chars[index]
            if inString {
                if escaped {
                    escaped = false
                } else if character == "\\" {
                    escaped = true
                } else if character == "\"" {
                    inString = false
                }
            } else {
                switch character {
                case "\"":
                    inString = true
                case "{":
                    depth += 1
                case "}":
                    depth -= 1
                    if depth == 0 {
                        return String(chars[start...index])
                    }
                default:
                    break
                }
            }
            index += 1
        }
        return nil
    }

    /// Decode `type` from the first JSON object embedded in `response`.
    static func decode<T: Decodable>(_ type: T.Type, from response: String) -> T? {
        guard
            let json = firstJSONObject(in: response),
            let data = json.data(using: .utf8)
        else {
            return nil
        }
        return try? JSONDecoder().decode(type, from: data)
    }
}
