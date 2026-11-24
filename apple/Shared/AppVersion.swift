import Foundation
#if !os(watchOS)
import UIKit
#endif

/// This app's version as people see it in Settings, and the system it runs on: `phone_device_info` reports them,
/// and `client_hello`'s `os` tells the hub (docs/protocol/protocol.md "Client capabilities and heartbeat"), which
/// keeps what each device last said for the assistant. The app carries no commit; the channel is the build
/// configuration.
nonisolated struct AppVersion: Equatable, Sendable {
    let version: String // CFBundleShortVersionString
    let build: String   // CFBundleVersion
    let channel: String // debug | release

    static let current = AppVersion(info: Bundle.main.infoDictionary ?? [:])

    init(info: [String: Any]) {
        version = info["CFBundleShortVersionString"] as? String ?? ""
        build = info["CFBundleVersion"] as? String ?? ""
        #if DEBUG
        channel = "debug"
        #else
        channel = "release"
        #endif
    }

    /// The system's version, "26.1" or "26.1.1".
    static var systemVersion: String {
        let v = ProcessInfo.processInfo.operatingSystemVersion
        return v.patchVersion == 0 ? "\(v.majorVersion).\(v.minorVersion)" : "\(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
    }

    /// The system's name; on an iPad "iPadOS", which tells the hub an iPad from an iPhone.
    @MainActor static var systemName: String {
        #if os(watchOS)
        "watchOS"
        #elseif os(visionOS)
        "visionOS"
        #elseif os(iOS)
        UIDevice.current.userInterfaceIdiom == .pad ? "iPadOS" : "iOS"
        #endif
    }

    /// `client_hello`'s `os`.
    @MainActor static var helloOS: [String: Any] { ["name": systemName, "version": systemVersion] }
}
