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
    static let modelPrefix = "gemma-4-E4B-it"
    /// The vision projector filename llama.cpp needs alongside the weights.
    static let mmprojName = "mmproj-F16.gguf"
    /// UserDefaults key holding the user's preferred weights filename.
    static let selectedModelKey = "selectedModelFileName"

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
    /// mmproj projector are eligible. If `preferred` is among the eligible files
    /// it wins; otherwise the first eligible file is used.
    static func chooseModelFile(candidates: [String], preferred: String?) -> String? {
        let eligible = candidates.filter {
            $0.hasPrefix(modelPrefix)
                && $0.lowercased().hasSuffix(".gguf")
                && !$0.lowercased().contains("mmproj")
        }
        if let preferred, eligible.contains(preferred) {
            return preferred
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

    /// Resolved URL to the vision projector file.
    var mmprojURL: URL? {
        for directory in searchDirectories {
            let url = directory.appendingPathComponent(Self.mmprojName)
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
