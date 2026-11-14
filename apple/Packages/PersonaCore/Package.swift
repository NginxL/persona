// swift-tools-version: 6.2
import PackageDescription

// The shared Rust core's Apple binding. Only the iOS app and its tests link this package; watch, widgets, Vision and
// Persona Link do not. Run native/app-core/scripts/build-apple.sh before `swift test` or
// xcodebuild: the XCFramework is an ignored build product, never a committed binary.
let package = Package(
    name: "PersonaCore",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [.library(name: "PersonaCore", targets: ["PersonaCore"])],
    targets: [
        .binaryTarget(name: "PersonaCoreC", path: "build/PersonaCoreC.xcframework"),
        .target(name: "PersonaCoreSerde", path: "Sources/Serde"),
        .target(
            name: "PersonaCoreWire",
            dependencies: ["PersonaCoreSerde"],
            path: "Sources/PersonaCoreWire"
        ),
        .target(
            name: "PersonaCore",
            dependencies: ["PersonaCoreC", "PersonaCoreWire", "PersonaCoreSerde"],
            path: "Sources/PersonaCore",
            swiftSettings: [
                .swiftLanguageMode(.v6),
                .defaultIsolation(nil),
                .enableExperimentalFeature("StrictConcurrency"),
                .unsafeFlags(["-warnings-as-errors"]),
            ]
        ),
        .testTarget(
            name: "PersonaCoreTests",
            dependencies: ["PersonaCore"],
            path: "Tests/PersonaCoreTests",
            swiftSettings: [
                .swiftLanguageMode(.v6),
                .defaultIsolation(nil),
                .enableExperimentalFeature("StrictConcurrency"),
                .unsafeFlags(["-warnings-as-errors"]),
            ]
        ),
    ]
)
