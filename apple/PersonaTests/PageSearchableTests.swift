import Foundation
import Testing

/// Pages give the window their search field through `pageSearchable` (TabActivity.swift): the Mac keeps every section's
/// page in the window (#390, `MacPageStack`), and a hidden page's plain `.searchable` still puts its field in the
/// window's toolbar, two of them a crash. Reads the sources next to this file, like `AllSessionsReadersTests`.
struct PageSearchableTests {
    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    @Test func pagesUsePageSearchable() {
        var found: [String] = []
        var scanned = 0
        for dir in ["Persona", "Modules", "Mac"] {
            let files = (FileManager.default.enumerator(at: Self.apple.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL } ?? []).filter { $0.pathExtension == "swift" && $0.lastPathComponent != "TabActivity.swift" }
            for file in files {
                scanned += 1
                let lines = ((try? String(contentsOf: file, encoding: .utf8)) ?? "").components(separatedBy: "\n")
                for (i, line) in lines.enumerated() {
                    let code = line.range(of: "//").map { String(line[..<$0.lowerBound]) } ?? line
                    if code.contains(".searchable(") { found.append("\(file.lastPathComponent):\(i + 1)") }
                }
            }
        }
        #expect(scanned > 100)
        #expect(found.isEmpty, "use .pageSearchable(…) instead of .searchable(…): \(found)")
    }
}
