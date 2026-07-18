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

    /// Return the first balanced `{ ... }` JSON object found in `text`, or nil.
    ///
    /// Braces that appear inside string literals (and escaped quotes) are ignored,
    /// so values like `{"note": "a}b"}` parse correctly.
    static func firstJSONObject(in text: String) -> String? {
        let cleaned = stripCodeFences(text)
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
