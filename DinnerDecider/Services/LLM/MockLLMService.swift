import CoreGraphics
import Foundation

/// Stand-in for the real on-device model.
///
/// It returns believable canned results after a short delay so the whole app is
/// navigable end to end. Importantly, its recipe output is a realistic JSON
/// string that flows through `LLMResponseParser`, exercising the exact code path
/// the real runtime will use.
final class MockLLMService: LLMService {

    private(set) var isLoaded = false

    /// Rotating pool of plausible grocery items for the mock scan.
    private let cannedItems: [IdentifiedItem] = [
        IdentifiedItem(name: "Whole Milk", brand: "Horizon", category: "dairy", confidence: 0.92),
        IdentifiedItem(name: "Large Eggs", brand: nil, category: "dairy", confidence: 0.88),
        IdentifiedItem(name: "Cheddar Cheese", brand: "Tillamook", category: "dairy", confidence: 0.9),
        IdentifiedItem(name: "Roma Tomatoes", brand: nil, category: "produce", confidence: 0.85),
        IdentifiedItem(name: "Baby Spinach", brand: "Earthbound", category: "produce", confidence: 0.83),
        IdentifiedItem(name: "Chicken Breast", brand: nil, category: "meat", confidence: 0.87),
        IdentifiedItem(name: "Spaghetti", brand: "Barilla", category: "pantry", confidence: 0.94),
        IdentifiedItem(name: "Black Beans", brand: "Bush's", category: "pantry", confidence: 0.9),
        IdentifiedItem(name: "Olive Oil", brand: "California Olive Ranch", category: "condiment", confidence: 0.89),
        IdentifiedItem(name: "Greek Yogurt", brand: "Fage", category: "dairy", confidence: 0.86)
    ]

    private var scanCallCount = 0

    func loadModel() async throws {
        try? await Task.sleep(nanoseconds: 400_000_000)
        isLoaded = true
    }

    func identifyItem(image: CGImage, ocrText: String) async throws -> IdentifiedItem {
        // Simulate per-crop inference latency so the scanning UI streams in.
        try? await Task.sleep(nanoseconds: 350_000_000)
        let item = cannedItems[scanCallCount % cannedItems.count]
        scanCallCount += 1
        return item
    }

    func generateText(prompt: String) async throws -> String {
        try? await Task.sleep(nanoseconds: 700_000_000)
        // Return JSON wrapped in prose + a code fence, exactly like a real model
        // tends to, so the parser is genuinely exercised.
        return """
        Sure! Based on what you have, here are some ideas:

        ```json
        {
          "makeNow": [
            {
              "name": "Cheesy Scrambled Eggs",
              "ingredients": [
                {"name": "Large Eggs", "hasIt": true},
                {"name": "Cheddar Cheese", "hasIt": true},
                {"name": "Whole Milk", "hasIt": true}
              ],
              "steps": [
                "Whisk the eggs with a splash of milk.",
                "Cook gently in a nonstick pan over low heat.",
                "Fold in grated cheddar just before serving."
              ],
              "timeMinutes": 10,
              "missingItems": []
            },
            {
              "name": "Simple Spaghetti Pomodoro",
              "ingredients": [
                {"name": "Spaghetti", "hasIt": true},
                {"name": "Roma Tomatoes", "hasIt": true},
                {"name": "Olive Oil", "hasIt": true}
              ],
              "steps": [
                "Boil the spaghetti until al dente.",
                "Simmer chopped tomatoes in olive oil.",
                "Toss the pasta in the sauce and serve."
              ],
              "timeMinutes": 20,
              "missingItems": []
            }
          ],
          "almostThere": [
            {
              "name": "Weeknight Black Bean Chili",
              "ingredients": [
                {"name": "Black Beans", "hasIt": true},
                {"name": "Roma Tomatoes", "hasIt": true},
                {"name": "Onion", "hasIt": false},
                {"name": "Chili Powder", "hasIt": false}
              ],
              "steps": [
                "Saute the onion until soft.",
                "Add tomatoes, beans, and chili powder.",
                "Simmer for 20 minutes and serve."
              ],
              "timeMinutes": 30,
              "missingItems": ["Onion", "Chili Powder"]
            }
          ]
        }
        ```

        Enjoy your dinner!
        """
    }
}
