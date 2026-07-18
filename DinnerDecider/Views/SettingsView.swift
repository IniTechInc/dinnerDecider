import SwiftUI

/// Preferences that shape recipe suggestions, plus model management.
struct SettingsView: View {
    @AppStorage(PrefKey.diet) private var diet = DietPreference.none.rawValue
    @AppStorage(PrefKey.allergies) private var allergies = ""
    @AppStorage(PrefKey.cuisineLikes) private var cuisineLikes = ""
    @AppStorage(ModelFileLocator.selectedModelKey) private var selectedModel = ""

    private let locator = ModelFileLocator()

    var body: some View {
        NavigationStack {
            Form {
                Section("Diet") {
                    Picker("Preference", selection: $diet) {
                        ForEach(DietPreference.allCases) { option in
                            Text(option.displayName).tag(option.rawValue)
                        }
                    }
                }

                Section("Allergies") {
                    TextField("e.g. peanuts, shellfish", text: $allergies, axis: .vertical)
                        .lineLimit(1...3)
                }

                Section {
                    TextField("e.g. Italian, Thai, Mexican", text: $cuisineLikes, axis: .vertical)
                        .lineLimit(1...3)
                } header: {
                    Text("Cuisines you like")
                } footer: {
                    Text("These preferences are added to the recipe prompt.")
                }

                Section("Model") {
                    let files = locator.availableModelFileNames()
                    if files.isEmpty {
                        Text("No model files found yet.")
                            .foregroundStyle(.secondary)
                    } else {
                        Picker("Weights file", selection: $selectedModel) {
                            Text("Auto (first available)").tag("")
                            ForEach(files, id: \.self) { file in
                                Text(file).tag(file)
                            }
                        }
                    }
                    NavigationLink {
                        ModelSetupView()
                    } label: {
                        Label("Model setup", systemImage: "cpu")
                    }
                }
            }
            .navigationTitle("Settings")
        }
    }
}

#Preview {
    SettingsView()
}
