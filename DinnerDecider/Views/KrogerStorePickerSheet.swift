import SwiftUI

/// A sheet that lets the user enter their zip code and pick a nearby Kroger store.
struct KrogerStorePickerSheet: View {
    @ObservedObject var kroger: KrogerService
    var onStoreSelected: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var zip = ""
    @State private var stores: [KrogerService.KrogerStore] = []
    @State private var isSearching = false

    var body: some View {
        NavigationStack {
            VStack(spacing: Spacing.lg) {
                Text("Find your Kroger store")
                    .font(.dmHeadline)
                    .padding(.top, Spacing.lg)

                HStack {
                    TextField("Zip code", text: $zip)
                        .keyboardType(.numberPad)
                        .textFieldStyle(.roundedBorder)
                    Button {
                        searchStores()
                    } label: {
                        if isSearching {
                            ProgressView()
                        } else {
                            Text("Search")
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(.brandPrimary)
                    .disabled(zip.count < 5 || isSearching)
                }
                .padding(.horizontal, Spacing.xl)

                if stores.isEmpty && !isSearching {
                    Spacer()
                    Text("Enter your zip code to find nearby stores.")
                        .font(.dmBody)
                        .foregroundStyle(.secondary)
                    Spacer()
                } else {
                    List(stores) { store in
                        Button {
                            kroger.selectStore(store)
                            dismiss()
                            onStoreSelected()
                        } label: {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(store.name)
                                    .font(.dmBody)
                                    .foregroundStyle(Color.textPrimary)
                                Text(store.address)
                                    .font(.dmCaption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                    .listStyle(.plain)
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func searchStores() {
        isSearching = true
        Task {
            stores = await kroger.searchStores(zipCode: zip)
            isSearching = false
        }
    }
}
