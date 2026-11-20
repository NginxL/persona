import XCTest

/// Reads the standalone host through the simulator's real accessibility bridge.
/// Element identifiers are test hooks only; labels and values must come from SwiftUI's native LabeledContent.
/// These are accessibility-tree assertions, not a claim that VoiceOver speech was listened to.
struct AboutAccessibilityFixture {
    let language: String
    let title: String
    let appearance: String
    let textSize: String
    let value = "1.42.0 (37)"
}

class AboutVersionAccessibilityTestCase: XCTestCase {
    override func setUp() {
        super.setUp()
        continueAfterFailure = false
    }

    @MainActor func verify(_ fixture: AboutAccessibilityFixture, sizeClass: UIUserInterfaceSizeClass) throws {
        let name = "\(fixture.language)-\(fixture.appearance)-\(fixture.textSize)-\(sizeClass == .regular ? "regular" : "compact")"
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(\(fixture.language))", "-AppleLocale", fixture.language,
                               "-aboutAccessibility.appearance", fixture.appearance,
                               "-aboutAccessibility.language", fixture.language,
                               "-aboutAccessibility.textSize", fixture.textSize]
        XCUIDevice.shared.appearance = fixture.appearance == "dark" ? .dark : .light
        app.launch()
        defer { app.terminate() }

        let native = row("about.native.version", in: app)
        let shared = row("about.shared.version", in: app)
        XCTAssertTrue(native.waitForExistence(timeout: 20), "native row absent: \(name)\n\(app.debugDescription)")
        XCTAssertTrue(shared.waitForExistence(timeout: 20), "shared row absent: \(name)\n\(app.debugDescription)")
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10))
        let actualSizeClass = app.descendants(matching: .any).matching(identifier: "about.host.size-class").firstMatch
        XCTAssertTrue(actualSizeClass.waitForExistence(timeout: 5))
        XCTAssertEqual(actualSizeClass.label, sizeClass == .regular ? "regular" : "compact",
                       "wrong real host size class: \(name)")
        print("ABOUT_HOST \(name) width=\(app.windows.firstMatch.frame.width) sizeClass=\(actualSizeClass.label)")

        // Native LabeledContent folds its value into this accessibility leaf's spoken label.
        // The locale controls the separator, so verify the actual title and value rather than English punctuation.
        let title = app.staticTexts[fixture.title].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 5), "localized native title absent: \(name)")
        for (kind, element) in [("native", native), ("shared", shared)] {
            XCTAssertEqual(element.elementType, .staticText, "\(kind) is not a readable static row: \(name)")
            XCTAssertTrue(element.label.hasPrefix(title.label), "\(kind) spoken title differs: \(name)")
            XCTAssertTrue(element.label.contains(fixture.value), "\(kind) spoken value missing: \(name)")
            XCTAssertGreaterThanOrEqual(element.frame.height, 44, "\(kind) row height: \(name)")
            XCTAssertFalse(element.label.contains("settings.about.version"), "raw shared key exposed: \(name)")
            print("ABOUT_A11Y \(kind) \(name) label=\(element.label.debugDescription)")
        }
        XCTAssertEqual(native.label, shared.label, "native/shared combined labels differ: \(name)")
        XCTAssertEqual(native.frame.width, shared.frame.width, accuracy: 0.01,
                       "native/shared cell width differs: \(name)")
        XCTAssertEqual(native.frame.height, shared.frame.height, accuracy: 0.01,
                       "native/shared cell height differs: \(name)")
    }

    @MainActor private func row(_ identifier: String, in app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: identifier).firstMatch
    }
}

final class AboutVersionAccessibilityCompactTests: AboutVersionAccessibilityTestCase {
    @MainActor func testNativeAndSharedExposeTheSameVersionLabelAndValue() throws {
        let fixtures = [
            AboutAccessibilityFixture(language: "en", title: "Version", appearance: "light", textSize: "standard"),
            AboutAccessibilityFixture(language: "en", title: "Version", appearance: "dark", textSize: "xxxlarge"),
            AboutAccessibilityFixture(language: "zh-Hans", title: "版本", appearance: "light", textSize: "xxxlarge"),
            AboutAccessibilityFixture(language: "zh-Hans", title: "版本", appearance: "dark", textSize: "standard"),
            AboutAccessibilityFixture(language: "ja", title: "バージョン", appearance: "light", textSize: "standard"),
            AboutAccessibilityFixture(language: "ja", title: "バージョン", appearance: "dark", textSize: "xxxlarge"),
        ]
        for fixture in fixtures { try verify(fixture, sizeClass: .compact) }
    }
}

final class AboutVersionAccessibilityRegularTests: AboutVersionAccessibilityTestCase {
    @MainActor func testNativeAndSharedExposeTheSameVersionLabelAndValue() throws {
        try verify(AboutAccessibilityFixture(language: "en", title: "Version",
                                             appearance: "light", textSize: "standard"), sizeClass: .regular)
    }
}
