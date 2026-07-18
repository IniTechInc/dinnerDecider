import SwiftUI

/// Preferences that shape recipe suggestions, plus model management and credits.
struct SettingsView: View {
    @AppStorage(PrefKey.diet) private var diet = DietPreference.none.rawValue
    @AppStorage(PrefKey.allergies) private var allergies = ""
    @AppStorage(PrefKey.cuisineLikes) private var cuisineLikes = ""
    @AppStorage(PrefKey.householdSize) private var householdSize = PrefKey.defaultHouseholdSize
    @AppStorage(ModelFileLocator.selectedModelKey) private var selectedModel = ""
    @AppStorage(PrefKey.debugSimulateFailure) private var simulateFailure = false

    private let locator = ModelFileLocator()

    @State private var debugTapCount = 0
    @State private var showDebug = false

    var body: some View {
        NavigationStack {
            Form {
                dietSection
                allergiesSection
                cuisinesSection
                householdSection
                modelSection
                aboutSection
                if showDebug {
                    debugSection
                }
            }
            .navigationTitle("Settings")
        }
    }

    // MARK: - Preferences

    private var dietSection: some View {
        Section("Diet") {
            Picker("Preference", selection: $diet) {
                ForEach(DietPreference.allCases) { option in
                    Text(option.displayName).tag(option.rawValue)
                }
            }
        }
    }

    private var allergiesSection: some View {
        Section {
            TokenField(text: $allergies, placeholder: "Add an allergy, e.g. peanuts")
        } header: {
            Text("Allergies")
        } footer: {
            Text("Recipes will avoid anything you list here.")
        }
    }

    private var cuisinesSection: some View {
        Section {
            TokenField(text: $cuisineLikes, placeholder: "Add a cuisine, e.g. Thai")
        } header: {
            Text("Cuisines you like")
        } footer: {
            Text("A gentle nudge toward flavours you enjoy.")
        }
    }

    private var householdSection: some View {
        Section {
            Stepper(value: $householdSize, in: 1...12) {
                HStack {
                    Label("People to cook for", systemImage: "person.2")
                    Spacer()
                    Text("\(householdSize)")
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }
            }
        } header: {
            Text("Household")
        } footer: {
            Text("Recipes are scaled to serve this many people.")
        }
    }

    // MARK: - Model management

    private var modelSection: some View {
        Section {
            statusRow(
                title: "Weights",
                present: locator.modelURL != nil,
                detail: locator.modelURL.map { "\($0.lastPathComponent) \(fileSize($0))" }
            )
            statusRow(
                title: "Vision projector",
                present: locator.mmprojURL != nil,
                detail: locator.mmprojURL.map { "\($0.lastPathComponent) \(fileSize($0))" }
            )

            let files = locator.availableModelFileNames()
            if !files.isEmpty {
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
        } header: {
            Text("On-device model")
        } footer: {
            Text(locator.isModelPresent
                 ? "Gemma 4 runs entirely on this device. No internet needed."
                 : "Add the model files to run fully on-device. Until then the app uses sample data.")
        }
    }

    private func statusRow(title: String, present: Bool, detail: String?) -> some View {
        HStack {
            Image(systemName: present ? "checkmark.circle.fill" : "xmark.circle")
                .foregroundStyle(present ? .green : .secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                if let detail, present {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text(present ? "Ready" : "Missing")
                .font(.caption.weight(.semibold))
                .foregroundStyle(present ? .green : .secondary)
        }
        .accessibilityElement(children: .combine)
    }

    private func fileSize(_ url: URL) -> String {
        guard
            let values = try? url.resourceValues(forKeys: [.fileSizeKey]),
            let size = values.fileSize
        else { return "" }
        return ByteCountFormatter.string(fromByteCount: Int64(size), countStyle: .file)
    }

    // MARK: - About

    private var aboutSection: some View {
        Section {
            creditRow(name: "Gemma 4", detail: "Google, Apache 2.0")
            creditRow(name: "LocalLLMClient", detail: "tattn, MIT")
            creditRow(name: "llama.cpp", detail: "ggml-org, MIT")

            HStack {
                Text("Version")
                Spacer()
                Text(appVersion)
                    .foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
            .onTapGesture(perform: registerDebugTap)
        } header: {
            Text("About")
        } footer: {
            Text("DinnerDecider recognises your food and suggests recipes, all on your device.")
        }
    }

    private func creditRow(name: String, detail: String) -> some View {
        HStack {
            Text(name)
            Spacer()
            Text(detail)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }

    private var appVersion: String {
        let version = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "1.0"
        let build = Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "1"
        return "\(version) (\(build))"
    }

    // MARK: - Hidden debug switch

    private var debugSection: some View {
        Section {
            Toggle(isOn: $simulateFailure) {
                Label("Simulate model error", systemImage: "ladybug")
            }
        } header: {
            Text("Developer")
        } footer: {
            Text("When on, Generate returns a broken reply so the recipe error and retry screen can be shown.")
        }
    }

    private func registerDebugTap() {
        debugTapCount += 1
        if debugTapCount >= 5 {
            withAnimation { showDebug = true }
            Haptics.success()
        }
    }
}

#Preview {
    SettingsView()
}
