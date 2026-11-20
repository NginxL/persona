import SwiftUI

/// The shade under a sidebar's selected row (#11032): one look for every row of a sidebar, the main conversation, the
/// places, Persona's chats and, in the Sidebar layout's column, the Agents list's rows. A rounded fill of
/// `SidebarSelectedShade.fill` in `Theme.Sidebar.radius`, drawn behind the row's content.
struct SidebarSelectedShade: View {
    /// On the Mac, the shade the shell's other lists select with (`macSelected`): the phone's `well` on the Mac's lighter
    /// fills (#1169) was hard to find there.
    #if os(macOS)
    static var fill: Color { Theme.Fill.macSelected } // the style's: read when drawn
    #else
    static let fill = Theme.Fill.well
    #endif

    var body: some View {
        RoundedRectangle(cornerRadius: Theme.Sidebar.radius, style: .continuous).fill(Self.fill)
    }
}
