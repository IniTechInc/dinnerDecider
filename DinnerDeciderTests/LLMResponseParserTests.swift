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
}
