// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "BeautifulUI",
    platforms: [.iOS(.v18), .macOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "BeautifulUI", targets: ["BeautifulUI"]),
    ],
    targets: [
        .target(name: "BeautifulUI"),
        .testTarget(name: "BeautifulUITests", dependencies: ["BeautifulUI"]),
    ]
)
