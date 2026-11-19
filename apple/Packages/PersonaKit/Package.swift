// swift-tools-version: 6.2
import PackageDescription

// The app's logic that needs no UI (#2095): its tests run with `swift test` on the Mac, without building the app or
// booting a simulator (docs/operations/testing.md「PersonaKit」). The apps import it; the watch and widgets do not (yet).
let package = Package(
    name: "PersonaKit",
    platforms: [.iOS(.v18), .macOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaKit", targets: ["PersonaKit"]),
    ],
    targets: [
        .target(name: "PersonaKit"),
        .testTarget(name: "PersonaKitTests", dependencies: ["PersonaKit"]),
    ]
)
