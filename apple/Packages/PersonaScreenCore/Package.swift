// swift-tools-version: 6.2
// PersonaScreenCore: what Persona Screen, the Mac's live-screen video helper (apple/PersonaLink), is built from (#7927,
// docs/extensions/computer-use.md "Live screen video (#3954)"): the video channel's wire protocol, the capture
// decisions and arguments, and the helper's own rate control, refinement and timing. Pure Swift with Foundation only.
// Kept apart from PersonaScreenKit, which re-exports it, so changes to the phone's and the browser's side (the route
// session, the quality tiers) don't change the helper's bytes: an ad-hoc signed helper's Screen Recording grant lasts
// only as long as its bytes do.
import PackageDescription

let package = Package(
    name: "PersonaScreenCore",
    platforms: [.macOS(.v15), .iOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaScreenCore", targets: ["PersonaScreenCore"])
    ],
    targets: [
        .target(name: "PersonaScreenCore", path: "Sources/PersonaScreenCore"),
        .testTarget(name: "PersonaScreenCoreTests", dependencies: ["PersonaScreenCore"], path: "Tests/PersonaScreenCoreTests")
    ],
    swiftLanguageModes: [.v5]
)
