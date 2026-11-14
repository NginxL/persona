// swift-tools-version: 6.2
// PersonaScreenKit: the iOS app's side of the live screen (#3954, docs/extensions/computer-use.md "Live screen video
// (#3954)"): the route session (#6398), the quality tiers (#6396) and the receiver's signals, on PersonaScreenCore's
// wire protocol and capture decisions, which it re-exports. Persona Screen (apple/PersonaLink) uses only
// PersonaScreenCore, so changes here never change its bytes (#7927). Pure Swift with Foundation only. The watch and
// the widgets don't use it.
import PackageDescription

let package = Package(
    name: "PersonaScreenKit",
    platforms: [.macOS(.v15), .iOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaScreenKit", targets: ["PersonaScreenKit"])
    ],
    dependencies: [
        .package(path: "../PersonaScreenCore")
    ],
    targets: [
        .target(name: "PersonaScreenKit", dependencies: [.product(name: "PersonaScreenCore", package: "PersonaScreenCore")],
                path: "Sources/PersonaScreenKit"),
        .testTarget(name: "PersonaScreenKitTests", dependencies: ["PersonaScreenKit"], path: "Tests/PersonaScreenKitTests")
    ],
    swiftLanguageModes: [.v5]
)
