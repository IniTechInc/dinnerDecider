import AuthenticationServices
import CryptoKit
import Foundation
import Security

/// Kroger API client — handles OAuth2 auth, product search, and cart operations.
/// Uses the same credentials as Skylite's Kroger integration.
@MainActor
final class KrogerService: ObservableObject {

    // MARK: - Configuration

    // Credentials load from KrogerCredentials.plist (git-ignored; keys
    // "clientId" and "clientSecret"). Never hardcode them: this file is in a
    // repo that will go public for the hackathon submission. When the plist is
    // absent the integration stays dormant and Settings shows it unconfigured.
    private static let credentials: (id: String, secret: String)? = {
        guard let url = Bundle.main.url(forResource: "KrogerCredentials", withExtension: "plist"),
              let data = try? Data(contentsOf: url),
              let dict = (try? PropertyListSerialization.propertyList(from: data, options: 0, format: nil)) as? [String: String],
              let id = dict["clientId"], let secret = dict["clientSecret"]
        else { return nil }
        return (id, secret)
    }()
    private static var clientId: String { credentials?.id ?? "" }
    private static var clientSecret: String { credentials?.secret ?? "" }
    /// False when no KrogerCredentials.plist is bundled; UI should hide or
    /// disable the Kroger connect flow.
    static var isConfigured: Bool { credentials != nil }
    private static let redirectURI = "dinnerdecider://kroger-callback"
    private static let scopes = "product.compact cart.basic:write"
    private static let baseURL = "https://api.kroger.com/v1"

    // MARK: - Published State

    @Published var isConnected = false
    @Published var storeName: String = ""
    @Published var storeId: String = ""
    @Published var isExporting = false
    @Published var exportError: String?
    @Published var exportSuccess = false

    // MARK: - Private

    private var accessToken: String?
    private var refreshToken: String?
    private var tokenExpiry: Date?

    init() {
        loadTokens()
        loadStorePreference()
    }

    // MARK: - OAuth2 Authorization Code Flow

    /// Kick off the Kroger login. Call from a view that can present the auth session.
    func connect() async -> URL? {
        let nonce = UUID().uuidString
        let state = signState(nonce: nonce)
        var components = URLComponents(string: "\(Self.baseURL)/connect/oauth2/authorize")!
        components.queryItems = [
            URLQueryItem(name: "scope", value: Self.scopes),
            URLQueryItem(name: "client_id", value: Self.clientId),
            URLQueryItem(name: "redirect_uri", value: Self.redirectURI),
            URLQueryItem(name: "response_type", value: "code"),
            URLQueryItem(name: "state", value: state)
        ]
        return components.url
    }

    /// Handle the OAuth callback URL after Kroger redirects back.
    func handleCallback(_ url: URL) async -> Bool {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let code = components.queryItems?.first(where: { $0.name == "code" })?.value
        else { return false }

        // Exchange code for tokens
        guard let tokens = await exchangeCode(code) else { return false }
        accessToken = tokens.access
        refreshToken = tokens.refresh
        tokenExpiry = Date().addingTimeInterval(tokens.expiresIn - 60)
        saveTokens()
        isConnected = true
        return true
    }

    /// Disconnect: clear tokens and state.
    func disconnect() {
        accessToken = nil
        refreshToken = nil
        tokenExpiry = nil
        isConnected = false
        deleteTokens()
    }

    // MARK: - Store Selection

    struct KrogerStore: Identifiable {
        let id: String
        let name: String
        let address: String
        let chain: String
    }

    func searchStores(zipCode: String) async -> [KrogerStore] {
        guard let token = await getClientToken() else { return [] }
        var components = URLComponents(string: "\(Self.baseURL)/locations")!
        components.queryItems = [
            URLQueryItem(name: "filter.zipCode.near", value: zipCode),
            URLQueryItem(name: "filter.limit", value: "5")
        ]
        guard let url = components.url else { return [] }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        guard let (data, _) = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let locations = json["data"] as? [[String: Any]]
        else { return [] }

        return locations.compactMap { loc in
            guard let id = loc["locationId"] as? String,
                  let name = loc["name"] as? String,
                  let address = loc["address"] as? [String: Any],
                  let line = address["addressLine1"] as? String,
                  let city = address["city"] as? String,
                  let state = address["state"] as? String
            else { return nil }
            let chain = loc["chain"] as? String ?? "Kroger"
            return KrogerStore(id: id, name: name, address: "\(line), \(city), \(state)", chain: chain)
        }
    }

    func selectStore(_ store: KrogerStore) {
        storeId = store.id
        storeName = store.name
        UserDefaults.standard.set(store.id, forKey: "krogerStoreId")
        UserDefaults.standard.set(store.name, forKey: "krogerStoreName")
    }

    // MARK: - Product Search & Cart

    struct KrogerProduct {
        let upc: String
        let name: String
        let brand: String
        let imageURL: String?
        let price: Double?
    }

    /// Search for a product by name at the selected store.
    func searchProduct(_ query: String) async -> KrogerProduct? {
        guard !storeId.isEmpty else { return nil }
        guard let token = await getValidToken() else { return nil }

        var components = URLComponents(string: "\(Self.baseURL)/products")!
        components.queryItems = [
            URLQueryItem(name: "filter.term", value: query),
            URLQueryItem(name: "filter.locationId", value: storeId),
            URLQueryItem(name: "filter.limit", value: "1")
        ]
        guard let url = components.url else { return nil }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        guard let (data, _) = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let products = json["data"] as? [[String: Any]],
              let first = products.first
        else { return nil }

        let upc = first["upc"] as? String ?? ""
        let description = first["description"] as? String ?? query
        let brand = first["brand"] as? String ?? ""

        // Extract price
        var price: Double?
        if let items = first["items"] as? [[String: Any]],
           let item = items.first,
           let priceInfo = item["price"] as? [String: Any] {
            price = priceInfo["promo"] as? Double ?? priceInfo["regular"] as? Double
        }

        // Extract image
        var imageURL: String?
        if let images = first["images"] as? [[String: Any]] {
            let front = images.first { ($0["perspective"] as? String) == "front" } ?? images.first
            if let sizes = front?["sizes"] as? [[String: Any]] {
                let large = sizes.first { ($0["size"] as? String) == "large" }
                imageURL = (large ?? sizes.first)?["url"] as? String
            }
        }

        return KrogerProduct(upc: upc, name: description, brand: brand, imageURL: imageURL, price: price)
    }

    /// Send a list of items to the Kroger cart. Searches for each, adds matched UPCs.
    func addToCart(items: [String]) async -> (added: Int, failed: Int) {
        isExporting = true
        exportError = nil
        exportSuccess = false

        guard let token = await getValidToken() else {
            exportError = "Not connected to Kroger. Please sign in."
            isExporting = false
            return (0, 0)
        }
        guard !storeId.isEmpty else {
            exportError = "Please select a Kroger store first."
            isExporting = false
            return (0, 0)
        }

        // Search for each item and collect UPCs
        var cartItems: [[String: Any]] = []
        var failed = 0

        for item in items {
            if let product = await searchProduct(item) {
                cartItems.append(["upc": product.upc, "quantity": 1])
            } else {
                failed += 1
            }
            // Rate limit: small delay between searches
            try? await Task.sleep(nanoseconds: 200_000_000)
        }

        guard !cartItems.isEmpty else {
            exportError = "Could not find any items at your Kroger store."
            isExporting = false
            return (0, failed)
        }

        // PUT to cart
        let url = URL(string: "\(Self.baseURL)/cart/add")!
        var request = URLRequest(url: url)
        request.httpMethod = "PUT"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let body: [String: Any] = ["items": cartItems]
        request.httpBody = try? JSONSerialization.data(withJSONObject: body)

        if let (_, response) = try? await URLSession.shared.data(for: request),
           let http = response as? HTTPURLResponse, http.statusCode < 300 {
            exportSuccess = true
            isExporting = false
            return (cartItems.count, failed)
        } else {
            exportError = "Failed to add items to Kroger cart. Please try again."
            isExporting = false
            return (0, items.count)
        }
    }

    // MARK: - Token Management

    /// Get a valid customer token, refreshing if expired.
    private func getValidToken() async -> String? {
        if let token = accessToken, let expiry = tokenExpiry, expiry > Date() {
            return token
        }
        // Try refresh
        guard let refresh = refreshToken else { return nil }
        guard let tokens = await refreshAccessToken(refresh) else {
            isConnected = false
            return nil
        }
        accessToken = tokens.access
        refreshToken = tokens.refresh
        tokenExpiry = Date().addingTimeInterval(tokens.expiresIn - 60)
        saveTokens()
        return tokens.access
    }

    /// Get a client credentials token (for product/location search without user login).
    private func getClientToken() async -> String? {
        let url = URL(string: "\(Self.baseURL)/connect/oauth2/token")!
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(Self.basicAuth(), forHTTPHeaderField: "Authorization")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.httpBody = "grant_type=client_credentials&scope=product.compact".data(using: .utf8)

        guard let (data, _) = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let token = json["access_token"] as? String
        else { return nil }
        return token
    }

    private struct TokenResponse {
        let access: String
        let refresh: String
        let expiresIn: TimeInterval
    }

    private func exchangeCode(_ code: String) async -> TokenResponse? {
        let url = URL(string: "\(Self.baseURL)/connect/oauth2/token")!
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(Self.basicAuth(), forHTTPHeaderField: "Authorization")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let body = "grant_type=authorization_code&code=\(code)&redirect_uri=\(Self.redirectURI)"
        request.httpBody = body.data(using: .utf8)

        guard let (data, _) = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let access = json["access_token"] as? String,
              let refresh = json["refresh_token"] as? String,
              let expires = json["expires_in"] as? Double
        else { return nil }
        return TokenResponse(access: access, refresh: refresh, expiresIn: expires)
    }

    private func refreshAccessToken(_ refreshToken: String) async -> TokenResponse? {
        let url = URL(string: "\(Self.baseURL)/connect/oauth2/token")!
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue(Self.basicAuth(), forHTTPHeaderField: "Authorization")
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        let body = "grant_type=refresh_token&refresh_token=\(refreshToken)"
        request.httpBody = body.data(using: .utf8)

        guard let (data, _) = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let access = json["access_token"] as? String,
              let refresh = json["refresh_token"] as? String,
              let expires = json["expires_in"] as? Double
        else { return nil }
        return TokenResponse(access: access, refresh: refresh, expiresIn: expires)
    }

    // MARK: - Helpers

    private static func basicAuth() -> String {
        let credentials = "\(clientId):\(clientSecret)"
        let encoded = Data(credentials.utf8).base64EncodedString()
        return "Basic \(encoded)"
    }

    private func signState(nonce: String) -> String {
        let payload = "\(nonce):\(Int(Date().timeIntervalSince1970))"
        let key = SymmetricKey(data: Data(Self.clientSecret.utf8))
        let signature = HMAC<SHA256>.authenticationCode(for: Data(payload.utf8), using: key)
        let sig = Data(signature).base64EncodedString()
        return "\(payload):\(sig)"
    }

    // MARK: - Keychain Storage

    private static let keychainService = "com.philwoolley.dinnerdecider.kroger"

    private func saveTokens() {
        save(key: "accessToken", value: accessToken ?? "")
        save(key: "refreshToken", value: refreshToken ?? "")
        if let expiry = tokenExpiry {
            save(key: "tokenExpiry", value: String(expiry.timeIntervalSince1970))
        }
    }

    private func loadTokens() {
        accessToken = load(key: "accessToken")
        refreshToken = load(key: "refreshToken")
        if let expiryStr = load(key: "tokenExpiry"), let interval = Double(expiryStr) {
            tokenExpiry = Date(timeIntervalSince1970: interval)
        }
        isConnected = refreshToken != nil && !refreshToken!.isEmpty
    }

    private func deleteTokens() {
        delete(key: "accessToken")
        delete(key: "refreshToken")
        delete(key: "tokenExpiry")
    }

    private func loadStorePreference() {
        storeId = UserDefaults.standard.string(forKey: "krogerStoreId") ?? ""
        storeName = UserDefaults.standard.string(forKey: "krogerStoreName") ?? ""
    }

    private func save(key: String, value: String) {
        let data = Data(value.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.keychainService,
            kSecAttrAccount as String: key
        ]
        SecItemDelete(query as CFDictionary)
        var add = query
        add[kSecValueData as String] = data
        SecItemAdd(add as CFDictionary, nil)
    }

    private func load(key: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.keychainService,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true
        ]
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private func delete(key: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.keychainService,
            kSecAttrAccount as String: key
        ]
        SecItemDelete(query as CFDictionary)
    }
}
