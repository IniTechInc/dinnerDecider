import SwiftUI

/// KAN-30: Settings — diet preferences, cuisine likes, allergies, model management.
struct SettingsView: View {
    @AppStorage("dietaryRestrictions") private var dietaryRestrictions = ""
    @AppStorage("cuisinePreferences") private var cuisinePreferences = ""
    @AppStorage("allergies") private var allergies = ""

    var modelFileSize: String {
        let fm = FileManager.default
        let gguf = modelsDirectory.appendingPathComponent("gemma-4-E4B-it-Q4_K_M.gguf")
        let mmproj = modelsDirectory.appendingPathComponent("mmproj-F16.gguf")
        var total: Int64 = 0
        if let attrs = try? fm.attributesOfItem(atPath: gguf.path) {
            total += attrs[.size] as? Int64 ?? 0
        }
        if let attrs = try? fm.attributesOfItem(atPath: mmproj.path) {
            total += attrs[.size] as? Int64 ?? 0
        }
        guard total > 0 else { return "Not downloaded" }
        return ByteCountFormatter.string(fromByteCount: total, countStyle: .file)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Diet Preferences") {
                    TextField("e.g. vegetarian, gluten-free", text: $dietaryRestrictions)
                }

                Section("Cuisine Preferences") {
                    TextField("e.g. Italian, Asian, Mexican", text: $cuisinePreferences)
                }

                Section("Allergies") {
                    TextField("e.g. nuts, shellfish, dairy", text: $allergies)
                }

                Section("Model Management") {
                    LabeledContent("Model", value: "Gemma 4 E4B (Q4_K_M)")
                    LabeledContent("Storage", value: modelFileSize)
                    Button("Delete Model Files", role: .destructive) {
                        deleteModelFiles()
                    }
                }
            }
            .navigationTitle("Settings")
        }
    }

    private func deleteModelFiles() {
        let fm = FileManager.default
        let files = ["gemma-4-E4B-it-Q4_K_M.gguf", "mmproj-F16.gguf"]
        for file in files {
            let url = modelsDirectory.appendingPathComponent(file)
            try? fm.removeItem(at: url)
        }
    }
}
