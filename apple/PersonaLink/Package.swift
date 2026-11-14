// swift-tools-version: 6.0
// Persona Screen: the independent Link install's recording helper, not the retired menu bar app.
import PackageDescription

let package = Package(
    name: "PersonaScreen",
    defaultLocalization: "en",
    platforms: [.macOS(.v15)],
    dependencies: [
        // The live screen's wire protocol and capture decisions, shared with the iOS app (#3954), and nothing else of the
        // phone's side: an ad-hoc signed helper's Screen Recording grant lasts only as long as its bytes do (#7927).
        .package(path: "../Packages/PersonaScreenCore")
    ],
    targets: [
        // Persona Screen: the iOS live-screen video helper (#3954, docs/extensions/computer-use.md), the app bundle the
        // node launches, capturing with ScreenCaptureKit and encoding with VideoToolbox; mac-link.sh embeds it in
        // the independently installed Link bundle. Its codec and decisions are PersonaScreenCore. `-S` leaves out debug
        // symbols, and with them this checkout's paths, so the same sources build to the same bytes anywhere (mac-link.sh
        // verify-screen).
        .executableTarget(name: "PersonaScreen", dependencies: [.product(name: "PersonaScreenCore", package: "PersonaScreenCore")],
                          path: "Sources/PersonaScreen", linkerSettings: [.unsafeFlags(["-Xlinker", "-S"])])
    ],
    swiftLanguageModes: [.v5]
)
