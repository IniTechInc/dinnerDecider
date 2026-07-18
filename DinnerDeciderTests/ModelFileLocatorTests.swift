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

    // MARK: - Memory-optimized default preference

    func testPrefersSmallerQuantOverQ4WhenBothPresent() {
        // No pinned preference: the memory-optimized Q3_K_S should win over the
        // larger Q4_0 to keep WIRED Metal memory down on device.
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: [
                "gemma-4-E4B-it-Q4_0.gguf",
                "gemma-4-E4B-it-Q3_K_S.gguf",
                "mmproj-gemma-4-E4B-it-Q8_0.gguf"
            ],
            preferred: nil
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q3_K_S.gguf")
    }

    func testUserPinnedFileStillWinsOverDefaultPreference() {
        // An explicit user selection always overrides the built-in preference.
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: [
                "gemma-4-E4B-it-Q4_0.gguf",
                "gemma-4-E4B-it-Q3_K_S.gguf"
            ],
            preferred: "gemma-4-E4B-it-Q4_0.gguf"
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q4_0.gguf")
    }

    func testPrefersIQ3XXSOverQ3AndQ4WhenAllPresent() {
        // Full preference order: the most memory-efficient quant (UD-IQ3_XXS)
        // wins over Q3_K_S and Q4_0 when nothing is pinned.
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: [
                "gemma-4-E4B-it-Q4_0.gguf",
                "gemma-4-E4B-it-Q3_K_S.gguf",
                "gemma-4-E4B-it-UD-IQ3_XXS.gguf"
            ],
            preferred: nil
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-UD-IQ3_XXS.gguf")
    }

    func testFallsBackToFirstEligibleWhenNoPreferredQuantPresent() {
        // Neither pinned nor a memory-optimized quant present: unchanged fallback
        // to the first eligible file.
        let chosen = ModelFileLocator.chooseModelFile(
            candidates: ["gemma-4-E4B-it-Q4_K_M.gguf", "gemma-4-E4B-it-Q4_0.gguf"],
            preferred: nil
        )
        XCTAssertEqual(chosen, "gemma-4-E4B-it-Q4_K_M.gguf")
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
