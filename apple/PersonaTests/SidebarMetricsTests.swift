import Foundation
import Testing
import UIKit
@testable import Persona

/// Sidebar rows take their size from `Theme.Sidebar` and nothing else (docs/apple/sidebar.md, #1647): the HIG row,
/// tall enough for any script, never a row height or padding written in the sidebar's own code.
struct SidebarMetricsTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    /// The files that draw sidebar rows. A new one goes here (docs/apple/sidebar.md, "Adding a row").
    static let files = [
        "Persona/Views/Assistant/AssistantSidebar.swift",
        "Persona/Views/Assistant/AssistantSidebarDirections.swift",
    ]

    private static func source(_ file: String) throws -> String {
        try String(contentsOf: apple.appending(path: file), encoding: .utf8)
    }

    private static func lines(_ file: String, matching pattern: String) throws -> [String] {
        let re = try NSRegularExpression(pattern: pattern)
        return try source(file).components(separatedBy: "\n").enumerated().compactMap { i, line in
            re.firstMatch(in: line, range: NSRange(line.startIndex..., in: line)) == nil ? nil : "\(file):\(i + 1): \(line.trimmingCharacters(in: .whitespaces))"
        }
    }

    /// A row's least height is `Theme.Sidebar.row` (a heading's is `HeadingMenu.minHeight`); no `avatarL` / `box50` /
    /// `tapTarget` or a number, which made each row pick its own size before.
    @Test func rowHeightsComeFromTheSidebarTokens() throws {
        let hits = try Self.files.flatMap {
            try Self.lines($0, matching: #"minHeight:(?! *(?:Theme\.Sidebar\.row\b|HeadingMenu\.minHeight\())"#)
        }
        #expect(hits.isEmpty, "use Theme.Sidebar.row (docs/apple/sidebar.md):\n\(hits.joined(separator: "\n"))")
    }

    /// Every row's least height comes with the sidebar's padding above and below its text, on the line before it.
    @Test func everyRowHasTheSidebarPadding() throws {
        var hits: [String] = []
        for file in Self.files {
            let lines = try Self.source(file).components(separatedBy: "\n")
            for (i, line) in lines.enumerated() where line.contains("minHeight: Theme.Sidebar.row") {
                if i == 0 || !lines[i - 1].contains(".padding(.vertical, Theme.Sidebar.rowPadding)") {
                    hits.append("\(file):\(i + 1)")
                }
            }
        }
        #expect(hits.isEmpty, "put .padding(.vertical, Theme.Sidebar.rowPadding) right before the row's minHeight:\n\(hits.joined(separator: "\n"))")
    }

    /// No row height, padding or font size as a number in the sidebar's code: text uses the `Theme.Font` levels
    /// (text styles, so Dynamic Type), sizes the tokens.
    @Test func noLiteralSizesOrFontPoints() throws {
        let hits = try Self.files.flatMap {
            try Self.lines($0, matching: #"(minHeight|height): *[0-9]|\.padding\(\.vertical, *[0-9]|\.system\(size:|\.font\(\.system\("#)
        }
        #expect(hits.isEmpty, "use Theme.Sidebar / Theme.Font (docs/apple/sidebar.md):\n\(hits.joined(separator: "\n"))")
    }

    /// The iPhone and iPad row is the system sidebar's 44 pt, and the smallest touch target.
    @Test func rowIsTheSystemSidebarRow() {
        #expect(Theme.Sidebar.row == 44)
        #expect(Theme.Sidebar.row >= Theme.Size.tapTarget)
    }

    /// One line of body text fits inside the row with its padding in both Latin and CJK fonts, at the default text
    /// size: the row's height is the token's, never the text's, and Chinese glyphs are not cut.
    @Test func bodyTextFitsTheRowInEveryScript() throws {
        let body = UIFont.preferredFont(forTextStyle: .body, compatibleWith: UITraitCollection(preferredContentSizeCategory: .large))
        let room = Theme.Sidebar.row - 2 * Theme.Sidebar.rowPadding
        let cjk = try #require(UIFont(name: "PingFangSC-Regular", size: body.pointSize))
        for font in [body, cjk] {
            #expect(font.lineHeight <= room, "\(font.fontName) at \(body.pointSize) pt: line \(font.lineHeight) > \(room)")
        }
    }
}
