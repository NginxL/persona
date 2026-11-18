import SwiftUI

// Approving a browser's sign-in (#10293, docs/protocol/protocol.md "Browser QR sign-in"): the two digits the iPhone
// shows for the user to type in the browser. A file of its own, away from Theme.swift. Shared/, so the watch and the
// widgets compile it; they draw none of it.

nonisolated extension Theme.Font {
    /// The number: 64 pt rounded bold with fixed-width digits, the same size at every text size. It is read off the
    /// screen to be typed elsewhere and is already far above the interface's largest size (docs/design/DESIGN_SYSTEM.md
    /// section 2.2, fixed-size exceptions).
    static let signInNumber = SwiftUI.Font.system(size: 64, weight: .bold, design: .rounded).monospacedDigit()
}
