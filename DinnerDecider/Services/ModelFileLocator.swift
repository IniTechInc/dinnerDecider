import Foundation

/// Finds the on-device model files (GGUF weights + vision projector).
///
/// Search order per file: the app's Documents directory first (so files dropped
/// in via Finder / File Sharing win), then the app bundle (for a bundled demo
/// build). A preferred weights file can be pinned via the
/// `selectedModelFileName` UserDefaults key so a differently named fine-tuned
/// GGUF can be swapped in later with no code change; if the preferred file is
/// missing we fall back to any matching file.
struct ModelFileLocator {

    /// Prefix every acceptable weights file must start with.
    /// Matches the real GGUF, e.g. `gemma-4-E4B-it-Q4_0.gguf`.
    static let modelPrefix = "gemma-4-E4B-it"

    /// Built-in weights preference, most-preferred first, used when the user has
    /// not pinned a file. Smaller quants are preferred over the larger Q4_0 /
    /// Q4_K_M builds because the whole model runs GPU-resident on device, so a
    /// smaller weights file is directly less WIRED Metal memory, which is what
    /// caused the on-device vm-pageshortage jetsam.
    ///
    /// Order is by measured trade-off in the macOS harness against this GGUF:
    /// - UD-IQ3_XXS (3.46 GiB): saves ~0.82 GiB of wired vs Q4_0, emitted the
    ///   cleanest/most compact JSON, and decoded *faster* than Q3_K_S on Apple
    ///   Metal (10.3 vs 8.8 tok/s) - so the usual "IQ is slow on Metal" caveat
    ///   did not hold on this hardware. Preferred.
    /// - Q3_K_S (3.60 GiB): saves ~0.68 GiB, also correct; kept as the fallback.
    static let defaultModelPreference = [
        "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
        "gemma-4-E4B-it-Q3_K_S.gguf"
    ]
    /// Preferred vision projector filename (the good, post-June-4 Q8_0 build).
    static let preferredMmprojName = "mmproj-gemma-4-E4B-it-Q8_0.gguf"
    /// UserDefaults key holding the user's preferred weights filename.
    static let selectedModelKey = "selectedModelFileName"

    /// Whether a filename is an acceptable vision projector (`mmproj*.gguf`).
    static func isMmproj(_ name: String) -> Bool {
        let lower = name.lowercased()
        return lower.hasPrefix("mmproj") && lower.hasSuffix(".gguf")
    }

    /// Choose the projector file from candidates, preferring the Q8_0 build.
    static func chooseMmprojFile(candidates: [String]) -> String? {
        let eligible = candidates.filter(isMmproj)
        if eligible.contains(preferredMmprojName) {
            return preferredMmprojName
        }
        return eligible.first
    }

    private let fileManager: FileManager
    private let defaults: UserDefaults

    init(fileManager: FileManager = .default, defaults: UserDefaults = .standard) {
        self.fileManager = fileManager
        self.defaults = defaults
    }

    // MARK: - Pure selection logic (unit tested)

    /// Choose the weights file from a list of candidate filenames.
    ///
    /// Only files that start with `modelPrefix`, end in `.gguf`, and are not the
    /// mmproj projector are eligible. Selection order:
    /// 1. the user's explicitly pinned `preferred` file, if present (this is how a
    ///    fine-tuned build is chosen, via Settings > Model);
    /// 2. the first available file from `defaultModelPreference` (memory-optimized
    ///    quants), so the smaller Q3_K_S wins over a larger Q4 build when both are
    ///    installed;
    /// 3. otherwise the first eligible file (unchanged fallback).
    static func chooseModelFile(candidates: [String], preferred: String?) -> String? {
        let eligible = candidates.filter {
            $0.hasPrefix(modelPrefix)
                && $0.lowercased().hasSuffix(".gguf")
                && !$0.lowercased().contains("mmproj")
        }
        if let preferred, eligible.contains(preferred) {
            return preferred
        }
        if let preferredQuant = defaultModelPreference.first(where: { eligible.contains($0) }) {
            return preferredQuant
        }
        return eligible.first
    }

    // MARK: - Filesystem resolution

    /// Directories searched, in priority order.
    var searchDirectories: [URL] {
        var directories: [URL] = []
        if let docs = try? fileManager.url(
            for: .documentDirectory, in: .userDomainMask, appropriateFor: nil, create: false
        ) {
            directories.append(docs)
        }
        if let bundle = Bundle.main.resourceURL {
            directories.append(bundle)
        }
        return directories
    }

    /// The user's pinned weights filename, if any.
    var preferredModelFileName: String? {
        defaults.string(forKey: Self.selectedModelKey)
    }

    /// All weights files (across search directories) the user could pick from.
    func availableModelFileNames() -> [String] {
        var names: [String] = []
        for directory in searchDirectories {
            guard let contents = try? fileManager.contentsOfDirectory(atPath: directory.path) else {
                continue
            }
            for name in contents
            where name.hasPrefix(Self.modelPrefix)
                && name.lowercased().hasSuffix(".gguf")
                && !name.lowercased().contains("mmproj") {
                if !names.contains(name) {
                    names.append(name)
                }
            }
        }
        return names
    }

    /// Resolved URL to the weights file, honouring the pinned preference.
    var modelURL: URL? {
        let candidates = availableModelFileNames()
        guard let chosen = Self.chooseModelFile(candidates: candidates, preferred: preferredModelFileName) else {
            return nil
        }
        for directory in searchDirectories {
            let url = directory.appendingPathComponent(chosen)
            if fileManager.fileExists(atPath: url.path) {
                return url
            }
        }
        return nil
    }

    /// All projector files (`mmproj*.gguf`) across the search directories.
    func availableMmprojFileNames() -> [String] {
        var names: [String] = []
        for directory in searchDirectories {
            guard let contents = try? fileManager.contentsOfDirectory(atPath: directory.path) else {
                continue
            }
            for name in contents where Self.isMmproj(name) && !names.contains(name) {
                names.append(name)
            }
        }
        return names
    }

    /// Resolved URL to the vision projector file (prefers the Q8_0 build).
    var mmprojURL: URL? {
        guard let chosen = Self.chooseMmprojFile(candidates: availableMmprojFileNames()) else {
            return nil
        }
        for directory in searchDirectories {
            let url = directory.appendingPathComponent(chosen)
            if fileManager.fileExists(atPath: url.path) {
                return url
            }
        }
        return nil
    }

    /// Both files present means the runtime can load a vision-capable model.
    var isModelPresent: Bool {
        modelURL != nil && mmprojURL != nil
    }
}
