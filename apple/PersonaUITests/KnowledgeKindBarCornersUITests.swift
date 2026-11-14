import XCTest

/// The Knowledge page's kind pills keep their round ends (#7735). The kind bar's row reaches the edges of its
/// inset-grouped cell, whose rounded corners clipped the first pill of the first and of the last row. Each kind is
/// picked in turn (the picked pill is the filled one in either style), and when the picked pill starts its row, its left
/// end is compared pixel by pixel with its own right end, mirrored: where the left end was cut, the page shows instead of
/// the pill. Plain style and BeautifulUI, light and dark and the largest text size, against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB). PERSONA_SHOTS=<dir> keeps a screenshot of each checked pill and what each check found
/// (PERSONA_SHOTS_PHASE names the run).
final class KnowledgeKindBarCornersUITests: XCTestCase {
    private var hub = ""
    private static let kinds = ["projects", "topics", "chats", "people", "docs", "meetings", "events", "recordings"]

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    @MainActor func testTheFirstPillsKeepTheirEndsInThePlainStyle() throws { try pillEnds(style: "glass") }

    @MainActor func testTheFirstPillsKeepTheirEndsInBeautifulUI() throws { try pillEnds(style: "beautiful") }

    // MARK: Helpers

    @MainActor private func pillEnds(style: String) throws {
        let looks = [("light", ["-ui.scheme", "light"]), ("dark", ["-ui.scheme", "dark"]),
                     ("light-ax5", ["-ui.scheme", "light", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])]
        var cut: [String] = []
        var log: [String] = []
        for (look, args) in looks {
            let app = XCUIApplication()
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "knowledge", "-ui.style", style] + args
            app.launchPinned()
            XCTAssertTrue(app.buttons["kb.kind.projects"].waitForExistence(timeout: 30), "no kind pills (\(style) \(look)):\n\(app.debugDescription)")
            var top = false, bottom = false
            for kind in Self.kinds {
                app.buttons["kb.kind." + kind].tap()
                let frames = try settled(app)
                guard let f = frames[kind] else { XCTFail("no \(kind) pill (\(style) \(look))"); return }
                XCTAssertTrue(app.buttons["kb.kind." + kind].isSelected, "\(kind) is not picked (\(style) \(look))")
                let row = frames.values.filter { abs($0.minY - f.minY) < 1 }
                guard abs(f.minX - row.map(\.minX).min()!) < 0.5 else { continue }
                top = top || abs(f.minY - frames.values.map(\.minY).min()!) < 1
                bottom = bottom || abs(f.minY - frames.values.map(\.minY).max()!) < 1
                let shot = app.screenshot()
                let pixels = try XCTUnwrap(Pixels(shot.pngRepresentation, pointWidth: app.frame.width))
                // BeautifulUI's picked chip has a 1 pt ring inside its outline (and its smoothing is coarser on a 2x iPad).
                let ends = pixels.unlikeEnds(f, margin: style == "beautiful" ? 2 : 0.5)
                log.append("\(look) \(kind) \(Self.text(f)): contrast \(Int(ends.contrast)), \(ends.unlike) of \(ends.compared) left-end pixels unlike the right end")
                XCTAssertGreaterThan(ends.contrast, 8, "the picked \(kind) pill can't be told from the page (\(style) \(look))")
                if ends.unlike > 0 { cut.append("\(look): \(kind) \(Self.text(f)) has \(ends.unlike) pixels of its left end cut") }
                keep(shot, "\(style)-\(look)-\(kind)")
            }
            XCTAssertTrue(top && bottom, "no picked pill started the first and the last row (\(style) \(look)):\n" + log.joined(separator: "\n"))
            app.terminate()
        }
        report(style: style, lines: log + (cut.isEmpty ? ["no pill end cut"] : cut))
        XCTAssertTrue(cut.isEmpty, "kind pills cut (\(style)):\n" + cut.joined(separator: "\n"))
    }

    /// Every pill's frame once no list is loading and the pick's animation is over.
    @MainActor private func settled(_ app: XCUIApplication) throws -> [String: CGRect] {
        let end = Date().addingTimeInterval(20)
        while app.activityIndicators.firstMatch.exists, Date() < end { Thread.sleep(forTimeInterval: 0.25) }
        Thread.sleep(forTimeInterval: 0.8)
        var out: [String: CGRect] = [:]
        func walk(_ e: XCUIElementSnapshot) {
            if e.identifier.hasPrefix("kb.kind."), e.elementType == .button { out[String(e.identifier.dropFirst(8))] = e.frame }
            e.children.forEach(walk)
        }
        walk(try app.snapshot())
        XCTAssertEqual(Set(out.keys), Set(Self.kinds), "not every kind pill is on the page")
        return out
    }

    private static func text(_ r: CGRect) -> String { String(format: "(%.1f, %.1f, w %.1f, h %.1f)", r.minX, r.minY, r.width, r.height) }

    private func report(style: String, lines: [String]) {
        let text = lines.joined(separator: "\n")
        print("kind pill ends (\(style)):\n" + text)
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try? text.write(toFile: dir + "/ends-\(style)-\(env["PERSONA_SHOTS_PHASE"] ?? "run").txt", atomically: true, encoding: .utf8)
    }

    private func keep(_ shot: XCUIScreenshot, _ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try? shot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(env["PERSONA_SHOTS_PHASE"] ?? "run").png"))
    }
}

/// A screenshot's pixels in sRGB.
private struct Pixels {
    let width: Int, height: Int, scale: CGFloat
    private var buffer: [UInt8]

    init?(_ png: Data, pointWidth: CGFloat) {
        guard let image = UIImage(data: png)?.cgImage, pointWidth > 0 else { return nil }
        width = image.width
        height = image.height
        scale = CGFloat(image.width) / pointWidth
        buffer = [UInt8](repeating: 0, count: width * height * 4)
        let drawn: Bool = buffer.withUnsafeMutableBytes { raw in
            guard let context = CGContext(data: raw.baseAddress, width: image.width, height: image.height, bitsPerComponent: 8,
                                          bytesPerRow: image.width * 4, space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                          bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            context.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
            return true
        }
        if !drawn { return nil }
    }

    /// The pixel under a point, as its largest channel apart from another's.
    private func at(_ x: CGFloat, _ y: CGFloat) -> [Int] {
        let px = min(max(Int(x * scale), 0), width - 1), py = min(max(Int(y * scale), 0), height - 1)
        let i = (py * width + px) * 4
        return [Int(buffer[i]), Int(buffer[i + 1]), Int(buffer[i + 2])]
    }

    private static func apart(_ a: [Int], _ b: [Int]) -> Int { zip(a, b).map { abs($0 - $1) }.max() ?? 0 }

    /// Compares the capsule's left end with its right end mirrored: how many pixels differ by more than half the pill's
    /// contrast with the page (a cut pixel shows the page). Only the outer 6 pt of the end, short of the padding before
    /// the icon or title, and only pixels `margin` or more inside the outline: clear of its smoothing (0.5 pt) and of a
    /// ring drawn inside it. The cut is shallow at the largest text size (about 1 pt deep), so the margin stays small.
    /// Contrast is the pill's fill against the page beside it.
    func unlikeEnds(_ f: CGRect, margin: CGFloat) -> (unlike: Int, compared: Int, contrast: Double) {
        let r = f.height / 2, inside = r - margin
        let fill = at(f.minX + 2.5, f.midY), page = at(f.minX - 4, f.midY)
        let contrast = Double(Self.apart(fill, page))
        var unlike = 0, compared = 0
        let step = 1 / scale
        var y = f.minY
        while y < f.maxY {
            var x = f.minX
            while x < f.minX + min(6, r) {
                let dx = f.minX + r - x, dy = y - f.midY
                if dx * dx + dy * dy <= inside * inside {
                    compared += 1
                    if Double(Self.apart(at(x, y), at(f.maxX - (x - f.minX), y))) > contrast / 2 { unlike += 1 }
                }
                x += step
            }
            y += step
        }
        return (unlike, compared, contrast)
    }
}
