// swift-tools-version: 6.0
// Persona Link: the menu bar app around persona-link on a Mac (docs/operations/link.md). scripts/app/mac-link.sh builds the .app.
import PackageDescription

let package = Package(
    name: "PersonaLink",
    defaultLocalization: "en",
    platforms: [.macOS(.v15)],
    targets: [
        // Resources/Localizable.xcstrings: the app's newer text in en / zh-Hans / ja (`linkText`, Attention.swift);
        // mac-link.sh copies the resource bundle into the app.
        .executableTarget(name: "PersonaLink", path: "Sources/PersonaLink", resources: [.process("Resources")],
                          swiftSettings: [.define("NATIVE_MIGRATION_STORAGE_ONLY")]),
        .testTarget(name: "PersonaLinkTests", dependencies: ["PersonaLink"], path: "Tests/PersonaLinkTests"),
        // Persona Screen: the iOS live-screen video helper (#3954, docs/extensions/computer-use.md). PersonaScreenKit
        // is the pure wire protocol (shared with the app later); PersonaScreen is the app bundle the node launches.
        // The real ScreenCaptureKit + VideoToolbox capture lands in the PoC on a real Mac; the skeleton stubs it.
        .target(name: "PersonaScreenKit", path: "Sources/PersonaScreenKit"),
        .executableTarget(name: "PersonaScreen", dependencies: ["PersonaScreenKit"], path: "Sources/PersonaScreen"),
        .testTarget(name: "PersonaScreenKitTests", dependencies: ["PersonaScreenKit"], path: "Tests/PersonaScreenKitTests")
    ],
    swiftLanguageModes: [.v5]
)
