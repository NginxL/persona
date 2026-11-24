import Foundation
import Testing
import PersonaWire
@testable import Persona

/// What a build was stamped with and how About and `phone_device_info` show it (#12926).
@MainActor struct BuildInfoTests {
    private let utc = TimeZone(identifier: "UTC")!
    private let labels = AppDebugInfo.Labels(
        unknown: "Unknown", version: "Version", commit: "Commit", branch: "Branch", builtAt: "Built At", builder: "Build Machine",
        xcode: "Xcode", swift: "Swift", configuration: "Configuration", bundleID: "Bundle ID", hub: "Hub",
        hubVersion: "Hub Version", hubConnection: "Hub Connection", launchedAt: "Launched At", device: "Device", system: "System")

    @Test func aBuildWithoutStampsShowsUnknownEverywhere() {
        let build = BuildInfo(info: [:])
        #expect(build == BuildInfo())
        #expect(build.commitLabel == nil)
        #expect(build.buildLabel(unknown: "Unknown") == "Unknown")
        let info = AppDebugInfo(app: .init(), build: build, runtime: .init(), device: .init())
        let rows = info.rows(labels, timeZone: utc)
        #expect(rows.count == 15)
        #expect(rows.allSatisfy { $0.value == "Unknown" })
        // Missing values are left out of the JSON, not written as null or "Unknown".
        let object = (try? JSONSerialization.jsonObject(with: Data(info.json.utf8))) as? [String: [String: Any]]
        #expect(object?.keys.sorted() == ["app", "build", "device", "runtime"])
        #expect(object?.values.allSatisfy(\.isEmpty) == true)
    }

    @Test func blankOrUnknownStampsCountAsMissing() {
        let build = BuildInfo(info: ["PersonaCommit": "unknown", "PersonaBuildInfo": ["commit": " ", "branch": "", "builtAt": "not a date"]])
        #expect(build.commit == nil)
        #expect(build.dirty == nil)
        #expect(build.branch == nil)
        #expect(build.builtAt == nil)
    }

    @Test func aDirtyTreeAddsDashDirtyAfterTheHash() {
        let debug = BuildInfo(info: ["PersonaBuildInfo": ["commit": "3518b00185", "dirty": true, "builtAt": "2026-11-22T15:00:07Z"]])
        #expect(debug.commitLabel == "3518b00185-dirty")
        #expect(debug.buildLabel(unknown: "Unknown", timeZone: utc) == "3518b00185-dirty · 11-22 15:00")
        let clean = BuildInfo(info: ["PersonaBuildInfo": ["commit": "3518b00185", "dirty": false, "builtAt": "2026-11-22T15:00:07Z"]])
        #expect(clean.buildLabel(unknown: "Unknown", timeZone: utc) == "3518b00185 · 11-22 15:00")
        // A Release stamp's "+" is the same mark; it has no build time.
        #expect(BuildInfo(info: ["PersonaCommit": "3518b001+"]).buildLabel(unknown: "Unknown") == "3518b001-dirty")
        #expect(BuildInfo(info: ["PersonaCommit": "3518b001"]).buildLabel(unknown: "Unknown") == "3518b001")
    }

    @Test func theBuildTimeIsShownInLocalTime() {
        let build = BuildInfo(info: ["PersonaBuildInfo": ["commit": "3518b00185", "builtAt": "2026-11-22T07:00:00Z"]])
        #expect(build.buildLabel(unknown: "Unknown", timeZone: TimeZone(secondsFromGMT: 8 * 3600)!) == "3518b00185 · 11-22 15:00")
    }

    @Test func theCopiedJSONIsTheEncodedStructWithSnakeCaseKeys() throws {
        let built = try Date("2026-11-22T07:00:00Z", strategy: .iso8601)
        let info = AppDebugInfo(
            app: .init(version: "1.0.0", build: "1"),
            build: BuildInfo(commit: "3518b00185", dirty: true, branch: "main", builtAt: built, builder: "build-mac",
                             xcode: "26.0 (17A324)", swift: "6.2", configuration: "Debug", bundleID: "com.example.persona"),
            runtime: .init(hubHost: "hub.example.com:8443", hubVersion: "0.9.0", hubConnection: "connected", launchedAt: built),
            device: .init(model: "iPhone17,1", system: "ios 26.0"))
        let object = try #require(JSONSerialization.jsonObject(with: Data(info.json.utf8)) as? [String: Any])
        let build = try #require(object["build"] as? [String: Any])
        #expect(build["commit"] as? String == "3518b00185")
        #expect(build["dirty"] as? Bool == true)
        #expect(build["built_at"] as? String == "2026-11-22T07:00:00Z")
        #expect(build["builder"] as? String == "build-mac")
        #expect(build["bundle_id"] as? String == "com.example.persona")
        let runtime = try #require(object["runtime"] as? [String: Any])
        #expect(runtime["hub_host"] as? String == "hub.example.com:8443")
        #expect(runtime["hub_version"] as? String == "0.9.0")
        #expect(runtime["hub_connection"] as? String == "connected")
        #expect(runtime["launched_at"] as? String == "2026-11-22T07:00:00Z")
        // The copied text decodes back to the same value: one struct, not a second format.
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        #expect(try decoder.decode(AppDebugInfo.self, from: Data(info.json.utf8)) == info)
        // The tool's `build` field is the same encoding.
        #expect(DeviceTool.json(info.build) == (try JSONDecoder().decode(JSONValue.self, from: JSONSerialization.data(withJSONObject: build))))
    }

    @Test func theHubIsItsHostAndPortOnlyNeverTheToken() {
        #expect(AppDebugInfo.host(URL(string: "https://me:secret@hub.example.com:8443/api?token=abc")) == "hub.example.com:8443")
        #expect(AppDebugInfo.host(URL(string: "http://[2001:db8::1]:7777")) == "[2001:db8::1]:7777")
        #expect(AppDebugInfo.host(URL(string: "https://hub.example.com")) == "hub.example.com")
        #expect(AppDebugInfo.host(nil) == nil)
    }
}
