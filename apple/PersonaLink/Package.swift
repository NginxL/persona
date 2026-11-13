// swift-tools-version: 6.0
// Persona Link: the menu bar app around persona-link on a Mac (docs/operations/link.md). scripts/app/mac-link.sh builds the .app.
import PackageDescription

let package = Package(
    name: "PersonaLink",
    defaultLocalization: "en",
    platforms: [.macOS(.v15)],
    dependencies: [
        // The live screen's wire protocol and capture decisions, shared with the iOS app (#3954).
        .package(path: "../Packages/PersonaScreenKit")
    ],
    targets: [
        // Resources/Localizable.xcstrings: the app's newer text in en / zh-Hans / ja (`linkText`, Attention.swift);
        // mac-link.sh copies the resource bundle into the app.
        .executableTarget(name: "PersonaLink", path: "Sources/PersonaLink", resources: [.process("Resources")],
                          swiftSettings: [.define("NATIVE_MIGRATION_STORAGE_ONLY")]),
        .testTarget(name: "PersonaLinkTests", dependencies: ["PersonaLink"], path: "Tests/PersonaLinkTests"),
        // Persona Screen: the iOS live-screen video helper (#3954, docs/extensions/computer-use.md), the app bundle the
        // node launches, capturing with ScreenCaptureKit and encoding with VideoToolbox; mac-link.sh embeds it in
        // Persona Link.app. Its codec and decisions are PersonaScreenKit (apple/Packages/PersonaScreenKit).
        .executableTarget(name: "PersonaScreen", dependencies: [.product(name: "PersonaScreenKit", package: "PersonaScreenKit")],
                          path: "Sources/PersonaScreen")
    ],
    swiftLanguageModes: [.v5]
)
