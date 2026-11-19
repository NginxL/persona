import Foundation
import Testing

/// Confirmation dialogs on pages use the design system's shared presentation rule (#8127). This reads the sources next
/// to this file, like `HoldSourceTests`, so a page cannot bypass that rule with SwiftUI's modifier directly.
struct ConfirmationDialogRuleTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    private static let directCall = try! NSRegularExpression(
        pattern: #"(?<![A-Za-z0-9_])confirmationDialog\s*\("#)

    private enum State {
        case code, lineComment, blockComment(Int), string(hashes: Int, quotes: Int)
    }

    private static func sources() throws -> [(name: String, text: String)] {
        let root = apple.appending(path: "Persona")
        let sharedRule = apple.appending(path: "Persona/DesignSystem/WindowTextSize.swift")
        let files = FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil)?
            .compactMap { $0 as? URL }
            .filter { $0.pathExtension == "swift" && $0.path != sharedRule.path }
            .sorted { $0.path < $1.path } ?? []
        #expect(files.count > 50, "the app's view sources are not at \(root.path)")
        return try files.map {
            (String($0.path.dropFirst(apple.path.count + 1)), try String(contentsOf: $0, encoding: .utf8))
        }
    }

    /// Blanks comments and string literals without moving newlines. Nested block comments and raw / multiline strings
    /// are handled; interpolation is left blank with the rest of its string.
    private static func code(_ text: String) -> String {
        let bytes = Array(text.utf8)
        var out = bytes
        var state = State.code
        var i = 0

        func matches(_ pattern: [UInt8], at index: Int) -> Bool {
            index + pattern.count <= bytes.count && Array(bytes[index..<(index + pattern.count)]) == pattern
        }
        func blank(_ range: Range<Int>) {
            for j in range where out[j] != 10 && out[j] != 13 { out[j] = 32 }
        }

        while i < bytes.count {
            switch state {
            case .code:
                if matches([47, 47], at: i) {
                    blank(i..<(i + 2)); state = .lineComment; i += 2
                } else if matches([47, 42], at: i) {
                    blank(i..<(i + 2)); state = .blockComment(1); i += 2
                } else {
                    var quote = i
                    while quote < bytes.count && bytes[quote] == 35 { quote += 1 }
                    if quote < bytes.count && bytes[quote] == 34 {
                        let hashes = quote - i
                        let quotes = matches([34, 34, 34], at: quote) ? 3 : 1
                        let length = hashes + quotes
                        blank(i..<(i + length)); state = .string(hashes: hashes, quotes: quotes); i += length
                    } else {
                        i += 1
                    }
                }
            case .lineComment:
                if bytes[i] == 10 || bytes[i] == 13 { state = .code } else { blank(i..<(i + 1)) }
                i += 1
            case .blockComment(let depth):
                if matches([47, 42], at: i) {
                    blank(i..<(i + 2)); state = .blockComment(depth + 1); i += 2
                } else if matches([42, 47], at: i) {
                    blank(i..<(i + 2)); state = depth == 1 ? .code : .blockComment(depth - 1); i += 2
                } else {
                    blank(i..<(i + 1)); i += 1
                }
            case .string(let hashes, let quotes):
                let delimiter = Array(repeating: UInt8(34), count: quotes) + Array(repeating: UInt8(35), count: hashes)
                let escape = [UInt8(92)] + Array(repeating: UInt8(35), count: hashes)
                if matches(delimiter, at: i) {
                    blank(i..<(i + delimiter.count)); state = .code; i += delimiter.count
                } else if matches(escape, at: i), i + escape.count < bytes.count {
                    blank(i..<(i + escape.count + 1)); i += escape.count + 1
                } else {
                    blank(i..<(i + 1)); i += 1
                }
            }
        }
        return String(decoding: out, as: UTF8.self)
    }

    private static func directCallLines(_ text: String) -> [Int] {
        let source = code(text) as NSString
        return directCall.matches(in: source as String, range: NSRange(location: 0, length: source.length)).compactMap {
            let prefix = source.substring(to: $0.range.location)
            let words = prefix.split { !($0.isLetter || $0.isNumber || $0 == "_") }
            if words.last == "func" { return nil }
            return 1 + prefix.reduce(0) { $0 + ($1 == "\n" ? 1 : 0) }
        }
    }

    /// The folders it reads are there (after a move, a check reading nothing passes for nothing).
    @Test func readsTheSources() throws {
        #expect(try Self.sources().count > 50)
    }

    @Test func pagesDoNotCallSwiftUIConfirmationDialogDirectly() throws {
        var found: [String] = []
        for file in try Self.sources() {
            found.append(contentsOf: Self.directCallLines(file.text).map { "\(file.name):\($0)" })
        }
        #expect(found.isEmpty, """
            present page confirmation dialogs through the design system's shared rule (#8127):
            \(found.joined(separator: "\n"))
            """)
    }

    @Test func findsCallsButNotDeclarationsCommentsOrStrings() {
        let sample = [
            #".confirmationDialog("#,
            #"content.confirmationDialog("#,
            #"return confirmationDialog ("#,
            #"// content.confirmationDialog("#,
            #"/* return confirmationDialog( */"#,
            #"let text = ".confirmationDialog(""#,
            #"let other = myconfirmationDialog("#,
            #"func confirmationDialog("#,
        ].joined(separator: "\n")
        #expect(Self.directCallLines(sample) == [1, 2, 3])
    }
}
