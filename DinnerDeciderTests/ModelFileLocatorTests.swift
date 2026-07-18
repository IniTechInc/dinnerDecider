import XCTest
@testable import DinnerDecider

final class ModelFileLocatorTests: XCTestCase {

    private let candidates = [
        "gemma-4-E4B-it-Q4_K_M.gguf",
        "gemma-4-E4B-it-finetuned-Q4_K_M.gguf",
        "mmproj-F16.gguf",
        "some-other-model.gguf",
        "README.txt"
    ]

    func testPrefersSelectedFileWhenPresent() {
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: candidates,
            preferred: "gemma-4-E4B-it-finetuned-Q4_K_M.gguf"
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-finetuned-Q4_K_M.gguf")
    }

    func testFallsBackToFirstMatchWhenPreferredMissing() {
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: candidates,
            preferred: "gemma-4-E4B-it-does-not-exist.gguf"
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q4_K_M.gguf")
    }

    func testFallsBackWhenNoPreferenceSet() {
        let chosen = ModelFileLocator.chooseModelFile(candidates: candidates, preferred: nil)
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q4_K_M.gguf")
    }

    func testExcludesMmprojAndNonMatchingFiles() {
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: ["mmproj-F16.gguf", "some-other-model.gguf", "README.txt"],
            preferred: nil
        )
        XCTAssertNil(chosen)
    }

    func testReturnsNilForEmptyCandidates() {
        XCTAssertNil(ModelFileLocator.chooseModelFile(candidates: [], preferred: "anything.gguf"))
    }
}
