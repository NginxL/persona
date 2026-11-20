import XCTest

/// A row of the Sidebar layout's column that says it is selected (`isSelected`, #11032): the main conversation, a place, a
/// Persona chat or an Agent's row; not the Agents' quick filters or other controls in the column.
struct SidebarSelectedRow: CustomStringConvertible {
    var id: String
    var label: String
    var description: String { "\(id) \"\(label)\"" }
}

extension XCUIApplication {
    /// Every sidebar row on screen that says it is selected, read from one snapshot.
    @MainActor func selectedSidebarRows() -> [SidebarSelectedRow] {
        guard let root = try? snapshot() else { return [] }
        var found: [SidebarSelectedRow] = []
        func walk(_ e: XCUIElementSnapshot) {
            let id = e.identifier
            let row = id.hasPrefix("sidebar-") || id == "agents.row" || id == "agents.compactRow"
            if row, e.isSelected { found.append(SidebarSelectedRow(id: id, label: e.label)) }
            e.children.forEach(walk)
        }
        walk(root)
        return found
    }

    /// Exactly one sidebar row is selected, `id` (whose label holds `text`), within a few seconds (a pick's update).
    @MainActor func assertOneSelectedSidebarRow(_ id: String, containing text: String? = nil, _ step: String,
                                                file: StaticString = #filePath, line: UInt = #line) {
        func right(_ rows: [SidebarSelectedRow]) -> Bool {
            rows.count == 1 && rows[0].id == id && (text.map { rows[0].label.contains($0) } ?? true)
        }
        var rows = selectedSidebarRows()
        let deadline = Date.now.addingTimeInterval(5)
        while !right(rows), Date.now < deadline {
            Thread.sleep(forTimeInterval: 0.3)
            rows = selectedSidebarRows()
        }
        XCTAssertTrue(right(rows), "\(step): the selected rows are \(rows), not \(id) \(text ?? "") alone:\n\(debugDescription)",
                      file: file, line: line)
    }
}
