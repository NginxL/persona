import SwiftUI

// A meeting both recorded and imported is one row in Knowledge's Meetings list (#8872, docs/design/DESIGN_SYSTEM.md
// "One home for meetings"): the recording's, its avatar marked in the corner with the app the meeting came from, as the
// Inbox marks a connector's item; its page links to the imported meeting's own notes there.

extension KBImportedMeeting {
    /// The mark of the app the meeting came from: Lark's for its connectors (the web's table has them so), another
    /// connector's own, nil for one with none (a plain link).
    var brand: String? {
        guard let source, !source.isEmpty else { return nil }
        return ["lark", "larkcli"].contains(source) ? Theme.Brand.lark : ConnectorText.brand(source)
    }

    /// What the link to its notes says: the app's name where this app knows the connector (Lark), else plainly.
    var notesTitle: String {
        brand == Theme.Brand.lark ? String(localized: "Lark Meeting Notes [Imported Meeting]")
            : String(localized: "Original Meeting Notes [Imported Meeting]")
    }
}

/// The corner of a recording's avatar in the meetings list: the app's own mark on white, a gray link without one.
struct KBImportedBadge: View {
    var imported: KBImportedMeeting

    var body: some View {
        Group {
            if let brand = imported.brand {
                BrandBadge(brand)
            } else {
                ThemedIcon(Theme.Icon.link)
                    .font(Theme.Font.labelHeavy).imageScale(.small).dynamicTypeSize(...DynamicTypeSize.large)
                    .foregroundStyle(.white)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(.secondary, in: .circle)
            }
        }
        .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
        .overlay(Circle().stroke(Theme.card, lineWidth: Theme.Size.lineMedium))
        .offset(x: Theme.Space.xs, y: Theme.Space.xs)
        .accessibilityHidden(true)
    }
}

/// The link to the imported meeting's own notes, on the recording's page: the app's mark (or a plain link) and its name.
struct KBImportedNotesButton: View {
    var imported: KBImportedMeeting
    var open: (URL) -> Void

    var body: some View {
        if let url = imported.url {
            Button { open(url) } label: {
                Label {
                    Text(imported.notesTitle).lineLimit(1)
                } icon: {
                    if let brand = imported.brand {
                        BrandMark(brand, side: Theme.Size.badge)
                    } else {
                        ThemedIcon(Theme.Icon.openDocument)
                    }
                }
            }
            .accessibilityIdentifier("kb-imported-notes")
        }
    }
}
