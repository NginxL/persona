// swift-tools-version: 6.2
// PersonaModuleAPI (#13432, part of #13430): the contract between the app's core and its feature modules. A module
// declares a descriptor and registers into typed slots; the core reads the resulting `ModuleRegistry` and never names a
// module's types. Foundation and SwiftUI only, no app code, so `swift test` runs on the Mac (docs/apple/ios.md, "Modules").
// The iOS and Vision apps import it; the watch, widgets and extensions do not (nothing in Shared/ uses it).
import PackageDescription

let package = Package(
    name: "PersonaModuleAPI",
    platforms: [.iOS(.v18), .macOS(.v15), .visionOS(.v26)],
    products: [
        .library(name: "PersonaModuleAPI", targets: ["PersonaModuleAPI"]),
    ],
    targets: [
        .target(name: "PersonaModuleAPI", path: "Sources/PersonaModuleAPI"),
        .testTarget(name: "PersonaModuleAPITests", dependencies: ["PersonaModuleAPI"], path: "Tests/PersonaModuleAPITests"),
    ]
)
