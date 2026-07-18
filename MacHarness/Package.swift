// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "MacHarness",
    platforms: [.macOS(.v14)],
    dependencies: [
        // 0.5.0 tag pinned by its exact commit revision. A version-based pin is
        // rejected by SwiftPM here because LocalLLMClientLlamaC uses unsafe build
        // flags; a revision pin references the identical 0.5.0 code and is allowed.
        .package(url: "https://github.com/tattn/LocalLLMClient.git", revision: "edc39ef2ffc1cef9cf856b0788de8d331f776c2e")
    ],
    targets: [
        .executableTarget(
            name: "MacHarness",
            dependencies: [
                .product(name: "LocalLLMClient", package: "LocalLLMClient"),
                .product(name: "LocalLLMClientLlama", package: "LocalLLMClient")
            ],
            swiftSettings: [.interoperabilityMode(.Cxx)]
        )
    ]
)
