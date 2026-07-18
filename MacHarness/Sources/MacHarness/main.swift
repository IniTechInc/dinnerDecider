import Foundation
import AppKit
import LocalLLMClient
import LocalLLMClientLlama

// Standalone macOS verification harness for the Gemma 4 E4B on-device runtime.
// Mirrors DinnerDecider's GemmaLLMService: ONE resident, grammar-free LlamaClient
// serving both image identification and text generation, with channel-stripping
// on the parse side. Proves the LocalLLMClient 0.5.0 package + the July 2026
// Gemma 4 chat template + our GGUF files all work together.
//
// Run:  swift run   (first build downloads the llama.cpp xcframework; model load ~15s)

let home = FileManager.default.homeDirectoryForCurrentUser.path
let snapshot = "\(home)/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/06f24bb269339b2a19a5167199b81e89ef813c10"
let modelURL = URL(fileURLWithPath: "\(snapshot)/gemma-4-E4B-it-Q4_0.gguf")
let mmprojURL = URL(fileURLWithPath: "\(snapshot)/mmproj-gemma-4-E4B-it-Q8_0.gguf")
let imagePath = "/private/tmp/claude-501/-Users-philwoolley-Projects-gemma4hackathon/e7d67faa-807c-441f-af53-fba52792188d/scratchpad/test_box.png"

func log(_ s: String) { FileHandle.standardError.write((s + "\n").data(using: .utf8)!) }

func downscale(_ image: NSImage, maxSide: CGFloat = 896) -> NSImage {
    let size = image.size
    let longest = max(size.width, size.height)
    guard longest > maxSide else { return image }
    let scale = maxSide / longest
    let newSize = NSSize(width: size.width * scale, height: size.height * scale)
    let out = NSImage(size: newSize)
    out.lockFocus()
    image.draw(in: NSRect(origin: .zero, size: newSize), from: NSRect(origin: .zero, size: size), operation: .copy, fraction: 1.0)
    out.unlockFocus()
    return out
}

/// Keep only the text after the last Gemma `<channel|>` close marker (safety net
/// for photos that trigger the thinking channel).
func stripReasoning(_ text: String) -> String {
    if let r = text.range(of: "<channel|>", options: .backwards) {
        return String(text[r.upperBound...])
    }
    return text
}

func complete(_ client: LlamaClient, prompt: String, image: NSImage? = nil) async throws -> (String, Int, Double) {
    let atts: [LLMAttachment] = image.map { [.image($0)] } ?? []
    let t = Date(); var out = ""; var n = 0
    for try await tok in try await client.textStream(from: .chat([.user(prompt, attachments: atts)])) {
        out += tok; n += 1
    }
    return (out, n, Date().timeIntervalSince(t))
}

func main() async {
    guard let raw = NSImage(contentsOfFile: imagePath) else { log("IMAGE MISSING"); exit(1) }
    let image = downscale(raw)

    log("Loading Gemma 4 E4B (grammar-free, single resident client)...")
    let t0 = Date()
    let client: LlamaClient
    do {
        client = try await LocalLLMClient.llama(
            url: modelURL, mmprojURL: mmprojURL,
            parameter: .init(context: 2048, batch: 512, temperature: 0.2, topK: 40, topP: 0.95)
        )
    } catch { log("MODEL LOAD FAILED: \(error)"); exit(2) }
    log(String(format: "Model loaded in %.1fs\n", Date().timeIntervalSince(t0)))

    let idPrompt = """
    You identify a single grocery item from the photo. Text found on the packaging: OATMEAL Original Flavor. \
    Respond ONLY with JSON, no other words: {"name": "...", "brand": "... or null", \
    "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}
    """
    // Two identify calls prove the resident client handles repeated calls.
    for i in 1...2 {
        do {
            let (out, n, dt) = try await complete(client, prompt: idPrompt, image: image)
            print("IDENTIFY #\(i): \(stripReasoning(out).trimmingCharacters(in: .whitespacesAndNewlines))")
            log(String(format: "  -> %d tokens in %.2fs (%.1f tok/s)", n, dt, Double(n)/dt))
        } catch { log("IDENTIFY #\(i) FAILED: \(error)"); exit(3) }
    }

    let recipePrompt = """
    You are a home cooking assistant. Inventory: eggs, milk, cheddar cheese, spinach, spaghetti, tomatoes.
    Respond ONLY with JSON: {"makeNow":[{"name":"","ingredients":[{"name":"","hasIt":true}],"steps":[""],"timeMinutes":0,"missingItems":[]}],"almostThere":[]}. Give 2 makeNow recipes.
    """
    do {
        let (out, n, dt) = try await complete(client, prompt: recipePrompt)
        print("\nRECIPE (first 240 chars): \(stripReasoning(out).trimmingCharacters(in: .whitespacesAndNewlines).prefix(240))")
        log(String(format: "  -> %d tokens in %.2fs (%.1f tok/s)", n, dt, Double(n)/dt))
    } catch { log("RECIPE FAILED: \(error)"); exit(4) }

    print("\n=== DONE ===")
}

await main()
