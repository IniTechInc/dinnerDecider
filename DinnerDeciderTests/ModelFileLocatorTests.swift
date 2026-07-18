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

    // MARK: - Real model filename matching

    func testChoosesRealQ4WeightsFile() {
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: ["gemma-4-E4B-it-Q4_0.gguf", "mmproj-gemma-4-E4B-it-Q8_0.gguf"],
            preferred: nil
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q4_0.gguf")
    }

    // MARK: - Projector selection

    func testPrefersQ8ProjectorWhenPresent() {
        let chosen = ModelFileLocator.chooseMmprojFile(candidates: [
            "mmproj-gemma-4-E4B-it-BF16.gguf",
            "mmproj-gemma-4-E4B-it-Q8_0.gguf"
        ])
        XCTAssertEqual(chosen, "mmproj-gemma-4-E4B-it-Q8_0.gguf")
    }

    func testFallsBackToAnyProjectorWhenPreferredMissing() {
        let chosen = ModelFileLocator.chooseMmprojFile(candidates: [
            "mmproj-F16.gguf",
            "gemma-4-E4B-it-Q4_0.gguf"
        ])
        XCTAssertEqual(chosen, "mmproj-F16.gguf")
    }

    func testReturnsNilWhenNoProjectorPresent() {
        XCTAssertNil(ModelFileLocator.chooseMmprojFile(candidates: [
            "gemma-4-E4B-it-Q4_0.gguf",
            "README.txt"
        ]))
    }
}
