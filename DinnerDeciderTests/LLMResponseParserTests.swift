import XCTest
@testable import DinnerDecider

final class LLMResponseParserTests: XCTestCase {

    func testDecodesJSONInsideCodeFences() throws {
        let response = """
        ```json
        {"name": "Whole Milk", "brand": "Horizon", "category": "dairy", "confidence": 0.92}
        ```
        """
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertNotNil(item)
        XCTAssertEqual(item?.name, "Whole Milk")
        XCTAssertEqual(item?.brand, "Horizon")
        XCTAssertEqual(item?.category, "dairy")
        XCTAssertEqual(item?.confidence ?? 0, 0.92, accuracy: 0.0001)
    }

    func testDecodesJSONWithLeadingProse() throws {
        let response = "Sure! Here is what I found: {\"name\": \"Large Eggs\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.8} Hope that helps."
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertNotNil(item)
        XCTAssertEqual(item?.name, "Large Eggs")
        XCTAssertNil(item?.brand)
    }

    func testReturnsNilForMalformedJSON() {
        XCTAssertNil(LLMResponseParser.firstJSONObject(in: "there is no json here at all"))
        XCTAssertNil(LLMResponseParser.firstJSONObject(in: "{ this never closes"))
        XCTAssertNil(LLMResponseParser.decode(IdentifiedItem.self, from: "{ not: valid json }"))
    }

    func testIgnoresBracesInsideStrings() throws {
        let response = "{\"name\": \"weird}name{here\", \"brand\": null, \"category\": \"other\", \"confidence\": 0.5}"
        let extracted = LLMResponseParser.firstJSONObject(in: response)
        XCTAssertEqual(extracted, response)
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertEqual(item?.name, "weird}name{here")
    }

    func testExtractsFirstBalancedObjectOnly() throws {
        let response = "{\"a\": {\"nested\": 1}} trailing {\"b\": 2}"
        let extracted = LLMResponseParser.firstJSONObject(in: response)
        XCTAssertEqual(extracted, "{\"a\": {\"nested\": 1}}")
    }

    // MARK: - Gemma 4 thinking-channel handling

    func testStripReasoningKeepsAnswerAfterLastChannelMarker() {
        let raw = "<|channel>thought\nThe box says OATMEAL so name is Oatmeal.\n<channel|>{\"name\": \"Oatmeal\"}"
        let stripped = LLMResponseParser.stripReasoning(raw)
        XCTAssertEqual(stripped, "{\"name\": \"Oatmeal\"}")
    }

    func testStripReasoningLeavesPlainTextUnchanged() {
        let raw = "{\"name\": \"Oatmeal\"}"
        XCTAssertEqual(LLMResponseParser.stripReasoning(raw), raw)
    }

    func testDecodesJSONAfterThinkingChannelPreamble() throws {
        // The reasoning text deliberately contains brace-like example JSON that
        // would fool a naive extractor; stripping the channel must discard it.
        let response = """
        <|channel>thought
        Let me build the JSON. Something like {"name": "WRONG"} but let me reconsider the category.
        <channel|>{"name": "Oatmeal", "brand": null, "category": "pantry", "confidence": 0.95}
        """
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertNotNil(item)
        XCTAssertEqual(item?.name, "Oatmeal")
        XCTAssertNil(item?.brand)
        XCTAssertEqual(item?.category, "pantry")
        XCTAssertEqual(item?.confidence ?? 0, 0.95, accuracy: 0.0001)
    }

    func testDecodesJSONWithThinkingChannelInsideCodeFence() throws {
        let response = """
        <|channel>thought
        Reasoning about the recipe.
        <channel|>```json
        {"name": "Omelet"}
        ```
        """
        let extracted = LLMResponseParser.firstJSONObject(in: response)
        XCTAssertEqual(extracted, "{\"name\": \"Omelet\"}")
    }

    // MARK: - JSON array parsing (identifyAllItems path)

    func testDecodesJSONArrayInsideCodeFences() {
        let response = """
        ```json
        [{"name": "Milk", "brand": null, "category": "dairy", "confidence": 0.92}]
        ```
        """
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertEqual(items?.count, 1)
        XCTAssertEqual(items?.first?.name, "Milk")
    }

    func testDecodesJSONArrayWithLeadingProse() {
        let response = "Here are the items: [{\"name\": \"Eggs\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.9}]"
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertEqual(items?.count, 1)
        XCTAssertEqual(items?.first?.name, "Eggs")
    }

    func testDecodesMultiItemArray() {
        let response = """
        [{"name": "Milk", "brand": "Horizon", "category": "dairy", "confidence": 0.9},
         {"name": "Eggs", "brand": null, "category": "dairy", "confidence": 0.85},
         {"name": "Butter", "brand": null, "category": "dairy", "confidence": 0.88}]
        """
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertEqual(items?.count, 3)
        XCTAssertEqual(items?[2].name, "Butter")
    }

    func testReturnsNilForObjectWhenArrayExpected() {
        let response = "{\"name\": \"Milk\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.9}"
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertNil(items)
    }

    func testReturnsNilForMalformedArray() {
        let response = "[{\"name\": \"Milk\"},"
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertNil(items)
    }

    func testDecodesArrayAfterThinkingChannel() {
        let response = """
        <|channel>thought
        I see several items in this fridge photo.
        <channel|>[{"name": "Yogurt", "brand": "Fage", "category": "dairy", "confidence": 0.91}]
        """
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertEqual(items?.count, 1)
        XCTAssertEqual(items?.first?.name, "Yogurt")
    }

    func testDecodesEmptyArray() {
        let response = "[]"
        let items = LLMResponseParser.decodeArray(IdentifiedItem.self, from: response)
        XCTAssertNotNil(items)
        XCTAssertEqual(items?.count, 0)
    }

    // MARK: - Missing / malformed fields

    func testMissingRequiredFieldReturnsNil() {
        let response = "{\"name\": \"Milk\"}"
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertNil(item)
    }

    func testNullBrandDecodesSuccessfully() {
        let response = "{\"name\": \"Eggs\", \"brand\": null, \"category\": \"produce\", \"confidence\": 0.8}"
        let item = LLMResponseParser.decode(IdentifiedItem.self, from: response)
        XCTAssertNotNil(item)
        XCTAssertNil(item?.brand)
    }
}
